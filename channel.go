package sse

import (
	"sync"
	"time"
)

const sendMessageToClientTimeout = 200 * time.Millisecond

// Channel represents a server sent events channel.
type Channel struct {
	mu          sync.RWMutex
	lastEventID string
	name        string
	clients     map[*Client]bool
}

func newChannel(name string) *Channel {
	return &Channel{
		sync.RWMutex{},
		"",
		name,
		make(map[*Client]bool),
	}
}

// SendMessage broadcast a message to all clients in a channel.
func (c *Channel) SendMessage(message *Message) {
	c.mu.Lock()
	c.lastEventID = message.id
	c.mu.Unlock()

	timer := time.NewTimer(sendMessageToClientTimeout)
	defer timer.Stop()

	c.mu.RLock()
	for c, open := range c.clients {
		if open {
			// we send message to client, but with timeout since it is possible for
			// the client channel message to be full, if we don't set timeout we will
			// block the entire publishing process
			select {
			case c.send <- message:
			case <-timer.C:
			}
			timer.Reset(sendMessageToClientTimeout)
		}
	}
	c.mu.RUnlock()
}

// Close closes the channel and disconnect all clients.
func (c *Channel) Close() {
	// Kick all clients of this channel.
	for client := range c.clients {
		c.removeClient(client)
	}
}

// ClientCount returns the number of clients connected to this channel.
func (c *Channel) ClientCount() int {
	c.mu.RLock()
	count := len(c.clients)
	c.mu.RUnlock()

	return count
}

// LastEventID returns the ID of the last message sent.
func (c *Channel) LastEventID() string {
	return c.lastEventID
}

func (c *Channel) addClient(client *Client) {
	c.mu.Lock()
	c.clients[client] = true
	c.mu.Unlock()
}

func (c *Channel) removeClient(client *Client) {
	c.mu.Lock()
	c.clients[client] = false
	delete(c.clients, client)
	c.mu.Unlock()
	close(client.send)
}
