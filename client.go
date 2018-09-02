package libtwitch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	logger "log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sync"
	"time"
)

var log = logger.New(os.Stdout, "libtwitch: ", 0)

const (
	oAuthTokenEndpoint = "https://id.twitch.tv/oauth2/token"
	apiEndpoint        = "https://api.twitch.tv/helix/"
)

type AccessToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"refresh_token"`
	Scope        string `json:"scope"`

	ExpiresAt time.Time
}

type Response struct {
	Data json.RawMessage `json:"data"`
}

type TwitchClient struct {
	ctx    context.Context
	cancel context.CancelFunc

	endpoint     string
	clientID     string
	clientSecret string

	token    *AccessToken
	tokenMtx sync.Mutex

	callbackURL       string
	callbackSecret    string
	streamWatchers    map[string]*StreamWatcher
	streamWatchersMtx sync.Mutex

	client http.Client
	debug  bool
}

// NewTwitchClient makes a twitch API client using the OAuth client credentials flow.
func NewTwitchClient(ctx context.Context, clientID, clientSecret, callbackURL string, debug bool) (*TwitchClient, error) {
	timeout := time.Duration(10 * time.Second)
	client := http.Client{
		Timeout: timeout,
	}

	secret, err := makeSecret()
	if err != nil {
		return nil, err
	}

	cctx, cancel := context.WithCancel(ctx)

	return &TwitchClient{
		ctx:    cctx,
		cancel: cancel,

		endpoint:     apiEndpoint,
		clientID:     clientID,
		clientSecret: clientSecret,

		callbackURL:    callbackURL,
		callbackSecret: secret,
		streamWatchers: make(map[string]*StreamWatcher),

		client: client,
		debug:  debug,
	}, nil
}

func (c *TwitchClient) log(format string, v ...interface{}) {
	if c.debug {
		if v != nil {
			format = fmt.Sprintf(format, v)
		}
		log.Println(format)
	}
}

func (c *TwitchClient) Close() {
	c.cancel()
}

func (c *TwitchClient) doRequest(request *http.Request) (*http.Response, []byte, error) {
	if c.debug {
		dump, _ := httputil.DumpRequest(request, true)
		fmt.Printf("\nREQUEST:\n%s\n\n", dump)
	}

	resp, err := c.client.Do(request)
	if err != nil {
		return nil, nil, err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	err = resp.Body.Close()
	if err != nil {
		return nil, nil, err
	}

	if c.debug {
		dump, _ := httputil.DumpResponse(resp, false)
		fmt.Printf("\nRESPONSE:\n%s%s\n\n", dump, string(body))
	}

	return resp, body, nil
}

// authenticate will fetch an OAuth2 access token with the given clientID and clientSecret, and
// add it as an "Authorization" header to the given request.
func (c *TwitchClient) authenticate(request *http.Request) error {

	if c.clientID == "" {
		return errors.New("client id is required")
	}

	if c.clientSecret == "" {
		c.log("auth: client secret not configured, using client id only")
		request.Header.Set("Client-ID", c.clientID)
		return nil
	}

	c.tokenMtx.Lock()
	defer c.tokenMtx.Unlock()

	if c.token != nil && c.token.ExpiresAt.Before(time.Now()) {
		c.log("using cached access token (expires: %s)", c.token.ExpiresAt)
		request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token.AccessToken))
		return nil
	}

	v := url.Values{
		"client_id":     []string{c.clientID},
		"client_secret": []string{c.clientSecret},
		"grant_type":    []string{"client_credentials"},
	}

	authRequest, err := http.NewRequest("POST", oAuthTokenEndpoint+"?"+v.Encode(), nil)
	if err != nil {
		return err
	}

	c.log("requesting new access token")
	resp, body, err := c.doRequest(authRequest)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("authentication failed: HTTP %d", resp.StatusCode)
	}

	t := &AccessToken{}
	err = json.Unmarshal(body, t)
	if err != nil {
		return err
	}

	t.ExpiresAt = time.Now().Add(time.Second * time.Duration(t.ExpiresIn))
	c.token = t
	c.log("using new access token (expires: %s)", c.token.ExpiresAt)
	request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token.AccessToken))
	return nil
}

func (c *TwitchClient) marshalResponse(b []byte) (*Response, error) {
	response := &Response{}
	if len(b) > 0 {
		err := json.Unmarshal(b, response)
		if err != nil {
			return nil, err
		}
	}
	return response, nil
}

func (c *TwitchClient) Request(method string, path string, params *url.Values, body interface{}) (*http.Response, []byte, error) {

	endpoint := c.endpoint + path
	if params != nil {
		endpoint = endpoint + "?" + params.Encode()
	}

	var bb *bytes.Buffer
	if body != nil {
		bb = bytes.NewBuffer(nil)
		enc := json.NewEncoder(bb)
		enc.SetEscapeHTML(false)
		err := enc.Encode(body)
		if err != nil {
			return nil, nil, err
		}
	} else {
		bb = bytes.NewBuffer(nil)
	}

	request, err := http.NewRequest(method, endpoint, bb)
	if err != nil {
		return nil, nil, err
	}

	err = c.authenticate(request)
	if err != nil {
		c.log("authentication failed: %s", err)
		return nil, nil, err
	}

	request.Header.Set("User-Agent", "libtwitch/v1")
	request.Header.Set("Content-Type", "application/json")

	resp, b, err := c.doRequest(request)
	if err != nil {
		return nil, nil, err
	}

	response, err := c.marshalResponse(b)
	if err != nil {
		return nil, nil, err
	}
	return resp, response.Data, nil
}
