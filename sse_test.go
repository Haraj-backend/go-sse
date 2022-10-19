package sse

import (
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
	chanName := "test-channel"
	msg := SimpleMessage("test-send")
	assert.Error(t, srv.SendMessage(chanName, msg))
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
	// require.NoError(t, srv.Start())
	// test send message, it should be received by all subscriber
}
