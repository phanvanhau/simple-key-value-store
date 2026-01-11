package e2e

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"

	"simplekv/tests/e2e/apiclient"
)

// APIError surfaces non-2xx responses from the server.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("api error: status=%d body=%s", e.StatusCode, e.Body)
}

//nolint:errorlint
func (e *APIError) Is(target error) bool {
	return target == ErrNotFound && e.StatusCode == http.StatusNotFound
}

var ErrNotFound = errors.New("not found")

type Client struct {
	api *apiclient.ClientWithResponses
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	var opts []apiclient.ClientOption
	if httpClient != nil {
		opts = append(opts, apiclient.WithHTTPClient(httpClient))
	}
	api, err := apiclient.NewClientWithResponses(baseURL, opts...)
	if err != nil {
		panic(fmt.Sprintf("failed to create api client: %v", err))
	}
	return &Client{api: api}
}

type KeyValue struct {
	Key   []byte
	Value []byte
}

// Put creates or overwrites a key with the given value.
func (c *Client) Put(ctx context.Context, key, value []byte) (KeyValue, error) {
	resp, err := c.api.PutKeyWithResponse(ctx, encodeBase64(key), apiclient.PutKeyJSONRequestBody{
		Value: encodeBase64(value),
	})
	if err != nil {
		return KeyValue{}, err
	}
	switch resp.StatusCode() {
	case http.StatusOK:
		if resp.JSON200 == nil {
			return KeyValue{}, fmt.Errorf("missing body for 200 response")
		}
		return decodeWireKeyValue(*resp.JSON200)
	case http.StatusBadRequest:
		return KeyValue{}, newAPIError(resp.StatusCode(), resp.Body)
	default:
		return KeyValue{}, newAPIError(resp.StatusCode(), resp.Body)
	}
}

// Get retrieves a key; returns ErrNotFound on 404.
func (c *Client) Get(ctx context.Context, key []byte) (KeyValue, error) {
	resp, err := c.api.GetKeyWithResponse(ctx, encodeBase64(key))
	if err != nil {
		return KeyValue{}, err
	}
	switch resp.StatusCode() {
	case http.StatusOK:
		if resp.JSON200 == nil {
			return KeyValue{}, fmt.Errorf("missing body for 200 response")
		}
		return decodeWireKeyValue(*resp.JSON200)
	case http.StatusNotFound:
		return KeyValue{}, ErrNotFound
	case http.StatusBadRequest:
		return KeyValue{}, newAPIError(resp.StatusCode(), resp.Body)
	default:
		return KeyValue{}, newAPIError(resp.StatusCode(), resp.Body)
	}
}

// Delete removes a key; idempotent on missing keys if the server returns 204.
func (c *Client) Delete(ctx context.Context, key []byte) error {
	resp, err := c.api.DeleteKeyWithResponse(ctx, encodeBase64(key))
	if err != nil {
		return err
	}
	switch resp.StatusCode() {
	case http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusBadRequest:
		return newAPIError(resp.StatusCode(), resp.Body)
	default:
		return newAPIError(resp.StatusCode(), resp.Body)
	}
}

// Scan returns ordered key/value pairs in [fromKey, endKey).
func (c *Client) Scan(ctx context.Context, fromKey, endKey []byte) ([]KeyValue, error) {
	resp, err := c.api.ScanRangeWithResponse(ctx, &apiclient.ScanRangeParams{
		FromKey: encodeBase64(fromKey),
		EndKey:  encodeBase64(endKey),
	})
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode() {
	case http.StatusOK:
		if resp.JSON200 == nil {
			return nil, fmt.Errorf("missing body for 200 response")
		}
		out := make([]KeyValue, 0, len(*resp.JSON200))
		for _, w := range *resp.JSON200 {
			kv, err := decodeWireKeyValue(w)
			if err != nil {
				return nil, err
			}
			out = append(out, kv)
		}
		return out, nil
	case http.StatusBadRequest:
		return nil, newAPIError(resp.StatusCode(), resp.Body)
	default:
		return nil, newAPIError(resp.StatusCode(), resp.Body)
	}
}

func encodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func decodeWireKeyValue(w apiclient.KeyValue) (KeyValue, error) {
	keyBytes, err := base64.StdEncoding.DecodeString(w.Key)
	if err != nil {
		return KeyValue{}, err
	}
	valBytes, err := base64.StdEncoding.DecodeString(w.Value)
	if err != nil {
		return KeyValue{}, err
	}
	return KeyValue{Key: keyBytes, Value: valBytes}, nil
}

func wrapAPIError(err error) error {
	return err
}

func newAPIError(status int, body []byte) error {
	if status == http.StatusNotFound {
		return ErrNotFound
	}
	return &APIError{
		StatusCode: status,
		Body:       string(body),
	}
}
