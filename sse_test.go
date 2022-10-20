package sse

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/context/ctxhttp"
)

func TestNewServerNilOptions(t *testing.T) {
	srv := NewServer(nil)
	defer srv.Shutdown()

	if srv == nil || srv.options == nil || srv.options.Logger == nil {
		t.Fail()
	}
}

func TestNewServerNilLogger(t *testing.T) {
	srv := NewServer(&Options{
		Logger: nil,
	})

	defer srv.Shutdown()

	if srv == nil || srv.options == nil || srv.options.Logger == nil {
		t.Fail()
	}
}

func TestServer(t *testing.T) {
	channelCount := 2
	clientCount := 5
	messageCount := 0

	srv := NewServer(&Options{
		Logger: log.New(os.Stdout, "go-sse: ", log.Ldate|log.Ltime|log.Lshortfile),
	})

	defer srv.Shutdown()

	// Create N channels
	for n := 0; n < channelCount; n++ {
		name := fmt.Sprintf("CH-%d", n+1)
		srv.addChannel(name)
		fmt.Printf("Channel %s registed\n", name)
	}

	wg := sync.WaitGroup{}
	m := sync.Mutex{}

	// Create N clients in all channes
	for n := 0; n < clientCount; n++ {
		for name, ch := range srv.channels {
			wg.Add(1)

			// Create new client
			c := newClient("", name)
			// Add client to current channel
			ch.addClient(c)

			id := fmt.Sprintf("C-%d", n+1)
			fmt.Printf("Client %s registed to channel %s\n", id, name)

			go func(id string, name string) {
				// Wait for messages in the channel
				for msg := range c.send {
					m.Lock()
					messageCount++
					m.Unlock()
					fmt.Printf("Channel: %s - Client: %s - Message: %s\n", name, id, msg.data)
					wg.Done()
				}
			}(id, name)
		}
	}

	// Send hello message to all channels and all clients in it
	srv.SendMessage("", SimpleMessage("hello"))

	srv.close()

	wg.Wait()

	if messageCount != channelCount*clientCount {
		t.Errorf("Expected %d messages but got %d", channelCount*clientCount, messageCount)
	}
}

func TestServerDontStartServer(t *testing.T) {
	// initialize server with don't start server option
	srv := NewServer(&Options{
		DontStartServer: true,
	})
	// try to send message, it should throw error since the server is not yet started
	assert.Error(t, srv.SendMessage("test-channel", SimpleMessage("test-send")))
	// try to restart server, it should throw error since the server is not yet started
	assert.Error(t, srv.Restart())
	// try to shutdown server, it should throw error since the server is not yet started
	assert.Error(t, srv.Shutdown())

	// try to serve http client, the client should receive internal status error, we set the
	// request timeout to 2 seconds
	wr := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "https://testing.com", nil)
	ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
	srv.ServeHTTP(wr, req.WithContext(ctx))
	assert.NoError(t, ctx.Err())
	assert.Equal(t, http.StatusInternalServerError, wr.Result().StatusCode)
	cancel()

	// start server, it should not throw error since server already started
	require.NoError(t, srv.Start())
	defer func() {
		assert.NoError(t, srv.Shutdown())
	}()
	// initialize http test server
	srvtest := httptest.NewServer(srv)
	defer srvtest.Close()
	// test send message, it should be received by all subscribers
	numChannels := 5
	numSubscribersEachChannel := 5
	countMessageReceived := 0

	var mux sync.Mutex
	incrCountMessageReceived := func() {
		mux.Lock()
		countMessageReceived++
		mux.Unlock()
	}

	var wgClientPrep, wgMessageReceived sync.WaitGroup

	for i := 0; i < numChannels; i++ {
		chanName := fmt.Sprintf("chan_%v", i)
		for j := 0; j < numSubscribersEachChannel; j++ {
			wgClientPrep.Add(1)
			wgMessageReceived.Add(1)
			go func() {
				// open client connection
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ul := fmt.Sprintf("%v/%v", srvtest.URL, chanName)
				resp, err := ctxhttp.Get(ctx, http.DefaultClient, ul)
				if err != nil {
					log.Printf("error: %v", err)
					return
				}
				defer resp.Body.Close()
				// wait for message
				sc := bufio.NewScanner(resp.Body)
				wgClientPrep.Done()
				for sc.Scan() {
					msg := sc.Text()
					// every sse message will be terminated with double newline (\n\n), when we are using scan
					// we already set the delimiter to be `\n`, which will resulted in empty string at the end
					if len(msg) == 0 {
						// increment count message
						incrCountMessageReceived()
						// close connection
						cancel()
						// mark message as received
						wgMessageReceived.Done()

						return
					}
				}
			}()
		}
	}

	// wait for all client to be ready receiving the message
	wgClientPrep.Wait()
	// publish message to all channels which ultimately lead to all clients
	assert.NoError(t, srv.SendMessage("", SimpleMessage("Hello World!")))
	// wait for all clients to complete
	wgMessageReceived.Wait()

	// check for number of message received
	assert.Equal(t, numChannels*numSubscribersEachChannel, countMessageReceived)
}
