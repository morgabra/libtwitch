package libtwitch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type TwitchClientError struct {
	OriginalError error
	message       string
}

func (e *TwitchClientError) Error() string {
	return e.message
}

func NewTwitchClientError(message string, originalError error) *TwitchClientError {
	return &TwitchClientError{
		message:       message,
		OriginalError: originalError,
	}
}

var ErrMultipleResults = NewTwitchClientError("multiple results found", nil)
var ErrNotFound = NewTwitchClientError("not found", nil)

type User struct {
	ID              string `json:"id"`
	Login           string `json:"login"`
	DisplayName     string `json:"display_name"`
	Type            string `json:"type"`
	BroadcasterType string `json:"broadcaster_type"`
	Description     string `json:"description"`
	ProfileImageURL string `json:"profile_image_url"`
	OfflineImageURL string `json:"offline_image_url"`
	ViewCount       int    `json:"view_count"`
	Email           string `json:"email,omitempty"`
}

type Stream struct {
	ID           string   `json:"id"`
	UserID       string   `json:"user_id"`
	GameID       string   `json:"game_id"`
	CommunityIDs []string `json:"community_ids"`
	Type         string   `json:"type"`
	Title        string   `json:"title"`
	ViewerCount  int      `json:"viewer_count"`
	StartedAt    string   `json:"started_at"`
	Language     string   `json:"language"`
	ThumbnailURL string   `json:"thumbnail_url"`
}

type Game struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BoxArtURL string `json:"box_art_url"`
}

type Follow struct {
	FromID     string `json:"from_id"`
	ToID       string `json:"to_id"`
	FollowedAt string `json:"followed_at"`
}

// TODO: copied a lot of code here, rethink this.
func (c *TwitchClient) getUser(k, v string) (*User, error) {
	resp, body, err := c.Request("GET", "users", &url.Values{k: []string{v}}, nil)
	if err != nil {
		return nil, NewTwitchClientError("error making request", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, NewTwitchClientError(fmt.Sprintf("unexpected status code: %d", resp.StatusCode), nil)
	}

	u := []*User{}
	err = json.Unmarshal(body, &u)
	if err != nil {
		return nil, NewTwitchClientError("failed to parse response", err)
	}

	if len(u) == 0 {
		return nil, ErrNotFound
	}

	if len(u) > 1 {
		return nil, ErrMultipleResults
	}

	return u[0], nil
}

func (c *TwitchClient) GetUserByName(name string) (*User, error) {
	return c.getUser("login", name)
}

func (c *TwitchClient) GetUserByID(id string) (*User, error) {
	return c.getUser("id", id)
}

func (c *TwitchClient) getGame(k, v string) (*Game, error) {
	resp, body, err := c.Request("GET", "games", &url.Values{k: []string{v}}, nil)
	if err != nil {
		return nil, NewTwitchClientError("error making request", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, NewTwitchClientError(fmt.Sprintf("unexpected status code: %d", resp.StatusCode), nil)
	}

	u := []*Game{}
	err = json.Unmarshal(body, &u)
	if err != nil {
		return nil, NewTwitchClientError("failed to parse response", err)
	}

	if len(u) == 0 {
		return nil, ErrNotFound
	}

	if len(u) > 1 {
		return nil, ErrMultipleResults
	}

	return u[0], nil
}

func (c *TwitchClient) GetGameByName(name string) (*Game, error) {
	return c.getGame("name", name)
}

func (c *TwitchClient) GetGameByID(id string) (*Game, error) {
	return c.getGame("id", id)
}

func (c *TwitchClient) getStream(k, v string) (*Stream, error) {
	resp, body, err := c.Request("GET", "streams", &url.Values{k: []string{v}}, nil)
	if err != nil {
		return nil, NewTwitchClientError("error making request", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, NewTwitchClientError(fmt.Sprintf("unexpected status code: %d", resp.StatusCode), nil)
	}

	u := []*Stream{}
	err = json.Unmarshal(body, &u)
	if err != nil {
		return nil, NewTwitchClientError("failed to parse response", err)
	}

	if len(u) == 0 {
		return nil, ErrNotFound
	}

	if len(u) > 1 {
		return nil, ErrMultipleResults
	}

	return u[0], nil
}

func (c *TwitchClient) GetStreamByUserName(name string) (*Stream, error) {
	return c.getStream("user_login", name)
}

func (c *TwitchClient) GetStreamByUserID(id string) (*Stream, error) {
	return c.getStream("user_id", id)
}
