package libtwitch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync"
	"time"
)

func makeTopicKey(topic, userID string) string {
	return fmt.Sprintf("%s/%s", topic, userID)
}

func makeTopicURL(topic, userID string) (string, error) {
	switch topic {
	case "streams":
		v := &url.Values{"user_id": []string{userID}}
		return fmt.Sprintf("https://api.twitch.tv/helix/streams?%s", v.Encode()), nil
	case "follows":
		v := &url.Values{
			"first": []string{"1"},
			"to_id": []string{userID},
		}
		return fmt.Sprintf("https://api.twitch.tv/helix/users/follows?%s", v.Encode()), nil
	default:
		return "", errors.New("invalid topic")
	}
}

func makeCallbackURL(callbackURL, topic, userID string) (string, error) {
	v := &url.Values{
		"user_id": []string{userID},
		"topic":   []string{topic},
	}
	return fmt.Sprintf("%s?%s", callbackURL, v.Encode()), nil
}

type WebhookSubscription struct {
	Mode     string `json:"hub.mode"`
	Topic    string `json:"hub.topic"`
	Callback string `json:"hub.callback"`
	Lease    string `json:"hub.lease_seconds,omitempty"`
	Secret   string `json:"hub.secret,omitempty"`
}

func (c *TwitchClient) WebhookHandler() http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {

		if c.debug {
			dump, _ := httputil.DumpRequest(r, true)
			c.log("\nWEBHOOK REQUEST:\n%s\n\n", dump)
		}

		mode := r.URL.Query().Get("hub.mode")
		switch mode {
		case "subscribe":
			c.log("webhook: Responding to subscription challenge. topic:%s", r.URL.Query().Get("hub.topic"))
			rw.Write([]byte(r.URL.Query().Get("hub.challenge")))
			return
		case "unsubscribe":
			c.log("webhook: Responding to unsubscription challenge. topic:%s", r.URL.Query().Get("hub.topic"))
			rw.Write([]byte(r.URL.Query().Get("hub.challenge")))
			return
		case "denied":
			// TODO: We should either not return a *StreamWatcher until we've answered the sub
			// challenge, or we should make a way to signal errors.
			c.removeStreamWatcher(r.URL.Query().Get("hub.topic"))
			c.log("webhook: Subscription denied\n")
			rw.WriteHeader(http.StatusOK)
			return
		default:

			topic := r.URL.Query().Get("topic")
			userID := r.URL.Query().Get("user_id")

			if topic == "" || userID == "" {
				c.log("webhook: Ignoring event - missing topic/user_id params")
				rw.WriteHeader(http.StatusOK)
				return
			}

			c.streamWatchersMtx.Lock()
			defer c.streamWatchersMtx.Unlock()

			sw, ok := c.streamWatchers[makeTopicKey(topic, userID)]
			if !ok {
				c.log("webhook(%s): Got event for topic with no active subscribers", makeTopicKey(topic, userID))
				rw.WriteHeader(http.StatusOK)
				return
			}

			body, err := ioutil.ReadAll(r.Body)
			if err != nil {
				c.log("webhook(%s): Cannot read request body: %s", sw.topicKey(), err.Error())
				rw.WriteHeader(http.StatusOK)
				return
			}

			response, err := c.marshalResponse(body)
			if err != nil {
				c.log("webhook(%s): Error parsing body: %s", sw.topicKey(), err.Error())
				rw.WriteHeader(http.StatusOK)
				return
			}

			switch topic {
			case "streams":
				streams := []*Stream{}
				err = json.Unmarshal(response.Data, &streams)
				if err != nil {
					c.log("webhook(%s): Error parsing body: %s", sw.topicKey(), err.Error())
					rw.WriteHeader(http.StatusOK)
					return
				}

				if len(streams) == 0 {
					c.log("webhook(%s): stream is down", sw.topicKey())
					select {
					case sw.streams <- nil:
					default:
					}
					rw.WriteHeader(http.StatusOK)
					return
				}

				c.log("webhook(%s): stream is up", sw.topicKey())
				for _, stream := range streams {
					select {
					case sw.streams <- stream:
					default:
					}
				}
				rw.WriteHeader(http.StatusOK)
				return

			case "follows":
				follows := []*Follow{}
				err = json.Unmarshal(response.Data, &follows)
				if err != nil {
					c.log("webhook(%s): Error parsing body: %s", sw.topicKey(), err.Error())
					rw.WriteHeader(http.StatusOK)
					return
				}

				if len(follows) == 0 {
					c.log("webhook(%s): empty follow event", sw.topicKey())
					rw.WriteHeader(http.StatusOK)
					return
				}

				for _, follow := range follows {
					c.log("webhook(%s): follow %s -> %s", sw.topicKey(), follow.FromID, follow.ToID)

					select {
					case sw.follows <- follow:
					default:
					}
				}
				rw.WriteHeader(http.StatusOK)
				return

			default:
				c.log("webhook: Got event for unknown topic: %s", topic)
				rw.WriteHeader(http.StatusOK)
				return
			}
		}
	}
}

type StreamWatcher struct {
	ctx    context.Context
	cancel context.CancelFunc

	client    *TwitchClient
	closeOnce sync.Once
	streams   chan *Stream
	follows   chan *Follow

	topic  string
	userID string
	lease  time.Duration
}

func (sw *StreamWatcher) topicKey() string {
	return makeTopicKey(sw.topic, sw.userID)
}

func (sw *StreamWatcher) resub() {

	// Resubscribe ~10% before our lease actually expires.
	// TODO: jitter-ticker?
	lease := time.Duration(float64(sw.lease) * .9)
	t := time.NewTicker(lease)

	for {
		select {
		case <-t.C:
			sw.client.log("streamwatcher(%s): re-subscribing to topic", sw.topicKey())
			err := sw.sub()
			if err != nil {
				sw.client.log("streamwatcher(%s): error re-subscribing to topic: %s", sw.topicKey(), err.Error())
			} else {
				sw.client.log("streamwatcher(%s): successfully re-subscribed to topic", sw.topicKey())
			}
		case <-sw.ctx.Done():
			sw.client.log("streamwatcher(%s): context canceled", sw.topicKey())
			t.Stop()
			return
		}
	}
}

func (sw *StreamWatcher) sub() error {

	topicURL, err := makeTopicURL(sw.topic, sw.userID)
	if err != nil {
		return err
	}

	cbURL, err := makeCallbackURL(sw.client.callbackURL, sw.topic, sw.userID)
	if err != nil {
		return err
	}

	sub := &WebhookSubscription{
		Mode:     "subscribe",
		Topic:    topicURL,
		Callback: cbURL,
		Lease:    strconv.Itoa(int(sw.lease.Seconds())),
		Secret:   sw.client.callbackSecret,
	}

	resp, _, err := sw.client.Request("POST", "webhooks/hub", nil, sub)
	if err != nil {
		return NewTwitchClientError("error making request", err)
	}

	if resp.StatusCode != http.StatusAccepted {
		return NewTwitchClientError(fmt.Sprintf("unexpected status code: %d", resp.StatusCode), nil)
	}

	return nil
}

func (sw *StreamWatcher) Close() {
	// TODO: Should this attempt an unsub? I don't think so, they don't live very long.
	// The hard part is you have to stick around long enough to handle an unsub webhook
	// challenge, so I think just an explicit unsub op seems better than trying to clean
	// up after ourselves.````````````
	sw.closeOnce.Do(func() {
		close(sw.streams)
		close(sw.follows)
		sw.cancel()
		sw.client.removeStreamWatcher(sw.topicKey())
	})
}

func (sw *StreamWatcher) Streams() <-chan *Stream {
	return sw.streams
}

func (sw *StreamWatcher) Follows() <-chan *Follow {
	return sw.follows
}

func (c *TwitchClient) addStreamWatcher(topic, userID string) (*StreamWatcher, error) {
	c.streamWatchersMtx.Lock()

	_, ok := c.streamWatchers[makeTopicKey(topic, userID)]
	if ok {
		// TODO: We can keep track of multiple subscribers to the same topic
		// with some added complexity here, but I don't need it for now.
		c.streamWatchersMtx.Unlock()
		return nil, errors.New("already subscribed to topic")
	}

	ctx, cancel := context.WithCancel(c.ctx)
	sw := &StreamWatcher{
		ctx:    ctx,
		cancel: cancel,

		client:  c,
		streams: make(chan *Stream, 5),
		follows: make(chan *Follow, 5),

		topic:  topic,
		userID: userID,

		lease: 10 * time.Minute,
	}
	c.streamWatchers[sw.topicKey()] = sw

	err := sw.sub()
	if err != nil {
		c.streamWatchersMtx.Unlock()
		c.removeStreamWatcher(sw.topicKey())
		return nil, err
	}

	go sw.resub()

	c.streamWatchersMtx.Unlock()
	return sw, nil
}

func (c *TwitchClient) removeStreamWatcher(topicKey string) error {
	c.streamWatchersMtx.Lock()
	defer c.streamWatchersMtx.Unlock()
	_, ok := c.streamWatchers[topicKey]
	if !ok {
		return nil
	}
	delete(c.streamWatchers, topicKey)
	return nil
}

// TODO: Ew. I hate this now. The first time a caller subs to something we should do the API
// sub dance, and then store a slice of channels for all the subsequent callers to recieve
// events on. This works well enough for now.
func (c *TwitchClient) WatchStream(userID string) (*StreamWatcher, error) {
	return c.addStreamWatcher("streams", userID)
}

func (c *TwitchClient) WatchFollows(userID string) (*StreamWatcher, error) {
	return c.addStreamWatcher("follows", userID)
}
