package sse

import (
	"errors"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

var (
	ErrServerNotStarted = errors.New("server is not started")
	ErrServerStarted    = errors.New("server is already started")
)

// Server represents a server sent events server.
type Server struct {
	mu           sync.RWMutex
	options      *Options
	channels     map[string]*Channel
	addClient    chan *Client
	removeClient chan *Client
	shutdown     chan bool
	closeChannel chan string
	isStarted    bool
}

// NewServer creates a new SSE server.
func NewServer(options *Options) *Server {
	if options == nil {
		options = &Options{
			Logger: log.New(os.Stdout, "go-sse: ", log.LstdFlags),
		}
	}

	if options.Logger == nil {
		options.Logger = log.New(ioutil.Discard, "", log.LstdFlags)
	}

	s := &Server{
		sync.RWMutex{},
		options,
		make(map[string]*Channel),
		make(chan *Client, 256), // we use buffered channel, to minimize blocking when sending signal
		make(chan *Client, 256), // we use buffered channel, to minimize blocking when sending signal
		make(chan bool),
		make(chan string),
		false,
	}

	// by default the server will start immediately, however sometimes we don't
	// want this behavior, this is why we have `Options.DontStartServer`.
	if !options.DontStartServer {
		s.Start()
	}

	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.isStarted {
		http.Error(w, "Server is not started", http.StatusInternalServerError)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported.", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	if s.options.hasHeaders() {
		for k, v := range s.options.Headers {
			h.Set(k, v)
		}
	}

	if r.Method == "GET" {
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		h.Set("X-Accel-Buffering", "no")

		var channelName string

		if s.options.ChannelNameFunc == nil {
			channelName = r.URL.Path
		} else {
			channelName = s.options.ChannelNameFunc(r)
		}

		lastEventID := r.Header.Get("Last-Event-ID")
		c := newClient(lastEventID, channelName)
		closeNotify := r.Context().Done()

		select {
		case s.addClient <- c:
		case <-closeNotify:
			return
		}

		// defer function to remove client from channel, here we give timeout
		// 1 second for inserting the request to s.removeClient.
		defer func() {
			select {
			case s.removeClient <- c:
			case <-time.After(1 * time.Second):
			}
		}()

		// send status ok header to client
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// stream event source to client
		for {
			select {
			case <-closeNotify:
				return
			case msg, ok := <-c.send:
				if !ok {
					return
				}
				msg.retry = s.options.RetryInterval
				w.Write(msg.Bytes())
				flusher.Flush()
			}
		}
	} else if r.Method != "OPTIONS" {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// SendMessage broadcast a message to all clients in a channel.
// If channelName is an empty string, it will broadcast the message to all channels.
func (s *Server) SendMessage(channelName string, message *Message) error {
	if !s.hasStarted() {
		return ErrServerNotStarted
	}
	if len(channelName) == 0 {
		s.options.Logger.Print("broadcasting message to all channels.")

		s.mu.RLock()

		for _, ch := range s.channels {
			ch.SendMessage(message)
		}

		s.mu.RUnlock()
	} else if ch, ok := s.getChannel(channelName); ok {
		ch.SendMessage(message)
		s.options.Logger.Printf("message sent to channel '%s'.", channelName)
	} else {
		s.options.Logger.Printf("message not sent because channel '%s' has no clients.", channelName)
	}

	return nil
}

// Start is used for starting the server
func (s *Server) Start() error {
	if s.hasStarted() {
		return ErrServerStarted
	}
	go s.dispatch()

	s.mu.Lock()
	s.isStarted = true
	s.mu.Unlock()

	return nil
}

// Restart closes all channels and clients and allow new connections.
func (s *Server) Restart() error {
	if !s.hasStarted() {
		return ErrServerNotStarted
	}
	s.options.Logger.Print("restarting server.")
	s.close()

	return nil
}

// Shutdown performs a graceful server shutdown.
func (s *Server) Shutdown() error {
	if !s.hasStarted() {
		return ErrServerNotStarted
	}
	s.shutdown <- true

	return nil
}

func (s *Server) hasStarted() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.isStarted
}

// ClientCount returns the number of clients connected to this server.
func (s *Server) ClientCount() int {
	i := 0

	s.mu.RLock()

	for _, channel := range s.channels {
		i += channel.ClientCount()
	}

	s.mu.RUnlock()

	return i
}

// HasChannel returns true if the channel associated with name exists.
func (s *Server) HasChannel(name string) bool {
	_, ok := s.getChannel(name)
	return ok
}

// GetChannel returns the channel associated with name or nil if not found.
func (s *Server) GetChannel(name string) (*Channel, bool) {
	return s.getChannel(name)
}

// Channels returns a list of all channels to the server.
func (s *Server) Channels() []string {
	channels := []string{}

	s.mu.RLock()

	for name := range s.channels {
		channels = append(channels, name)
	}

	s.mu.RUnlock()

	return channels
}

// CloseChannel closes a channel.
func (s *Server) CloseChannel(name string) {
	s.closeChannel <- name
}

func (s *Server) addChannel(name string) *Channel {
	ch := newChannel(name)

	s.mu.Lock()
	s.channels[ch.name] = ch
	s.mu.Unlock()

	s.options.Logger.Printf("channel '%s' created.", ch.name)

	return ch
}

func (s *Server) removeChannel(ch *Channel) {
	s.mu.Lock()
	delete(s.channels, ch.name)
	s.mu.Unlock()

	ch.Close()

	s.options.Logger.Printf("channel '%s' closed.", ch.name)
}

func (s *Server) getChannel(name string) (*Channel, bool) {
	s.mu.RLock()
	ch, ok := s.channels[name]
	s.mu.RUnlock()
	return ch, ok
}

func (s *Server) close() {
	for _, ch := range s.channels {
		s.removeChannel(ch)
	}
}

func (s *Server) dispatch() {
	s.options.Logger.Print("server started.")

	for {
		select {

		// New client connected.
		case c := <-s.addClient:
			ch, exists := s.getChannel(c.channel)

			if !exists {
				ch = s.addChannel(c.channel)
			}

			ch.addClient(c)
			s.options.Logger.Printf("new client connected to channel '%s'.", ch.name)

		// Client disconnected.
		case c := <-s.removeClient:
			if ch, exists := s.getChannel(c.channel); exists {
				ch.removeClient(c)
				s.options.Logger.Printf("client disconnected from channel '%s'.", ch.name)

				if ch.ClientCount() == 0 {
					s.options.Logger.Printf("channel '%s' has no clients.", ch.name)
					s.removeChannel(ch)
				}
			}

		// Close channel and all clients in it.
		case channel := <-s.closeChannel:
			if ch, exists := s.getChannel(channel); exists {
				s.removeChannel(ch)
			} else {
				s.options.Logger.Printf("requested to close nonexistent channel '%s'.", channel)
			}

		// Event Source shutdown.
		case <-s.shutdown:
			s.close()
			close(s.addClient)
			close(s.removeClient)
			close(s.closeChannel)
			close(s.shutdown)

			s.options.Logger.Print("server stopped.")
			return
		}
	}
}
