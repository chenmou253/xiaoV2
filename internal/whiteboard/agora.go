package whiteboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Agora struct {
	AppIdentifier, AccessKey, SecretKey, Region string
	HTTP                                        *http.Client
}

func (a Agora) configured() bool {
	return a.AppIdentifier != "" && a.AccessKey != "" && a.SecretKey != "" && a.Region != ""
}

func (a Agora) request(ctx context.Context, method, path, token string, payload any) ([]byte, error) {
	if !a.configured() {
		return nil, errors.New("Agora whiteboard is not configured")
	}
	var body io.Reader
	if payload != nil {
		b, e := json.Marshal(payload)
		if e != nil {
			return nil, e
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, "https://api.netless.link/v5"+path, body)
	if e != nil {
		return nil, e
	}
	if token != "" {
		req.Header.Set("token", token)
	}
	req.Header.Set("region", a.Region)
	req.Header.Set("Content-Type", "application/json")
	client := a.HTTP
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	response, e := client.Do(req)
	if e != nil {
		return nil, e
	}
	defer response.Body.Close()
	b, e := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if e != nil {
		return nil, e
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("whiteboard HTTP %d: %s", response.StatusCode, string(b))
	}
	return b, nil
}

func unwrap(raw []byte) []byte {
	var envelope struct {
		Body json.RawMessage `json:"body"`
	}
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Body) > 0 {
		return envelope.Body
	}
	return raw
}

func parseToken(raw []byte, prefix string) (string, error) {
	var token string
	if err := json.Unmarshal(unwrap(raw), &token); err != nil {
		return "", err
	}
	if !strings.HasPrefix(token, prefix) {
		return "", errors.New("invalid whiteboard token response")
	}
	return token, nil
}

func (a Agora) sdkToken(ctx context.Context) (string, error) {
	raw, e := a.request(ctx, http.MethodPost, "/tokens/teams", "", map[string]any{"accessKey": a.AccessKey, "secretAccessKey": a.SecretKey, "lifespan": 3600000, "role": "admin"})
	if e != nil {
		return "", e
	}
	return parseToken(raw, "NETLESSSDK_")
}

func (a Agora) CreateRoom(ctx context.Context) (string, error) {
	token, e := a.sdkToken(ctx)
	if e != nil {
		return "", e
	}
	raw, e := a.request(ctx, http.MethodPost, "/rooms", token, map[string]any{"isRecord": false, "limit": 0})
	if e != nil {
		return "", e
	}
	var out struct {
		UUID string `json:"uuid"`
	}
	if e = json.Unmarshal(unwrap(raw), &out); e != nil {
		return "", e
	}
	if out.UUID == "" {
		return "", errors.New("whiteboard room response lacks UUID")
	}
	return out.UUID, nil
}

func (a Agora) RoomToken(ctx context.Context, uuid string, until time.Time) (string, error) {
	lifespan := int(time.Until(until).Seconds()) * 1000
	if lifespan <= 0 {
		return "", errors.New("lesson has ended")
	}
	token, e := a.sdkToken(ctx)
	if e != nil {
		return "", e
	}
	raw, e := a.request(ctx, http.MethodPost, "/tokens/rooms/"+url.PathEscape(uuid), token, map[string]any{"lifespan": lifespan, "role": "writer"})
	if e != nil {
		return "", e
	}
	return parseToken(raw, "NETLESSROOM_")
}

func (a Agora) DisableRoom(ctx context.Context, uuid string) error {
	token, e := a.sdkToken(ctx)
	if e != nil {
		return e
	}
	_, e = a.request(ctx, http.MethodPatch, "/rooms/"+url.PathEscape(uuid), token, map[string]any{"isBan": true})
	return e
}
