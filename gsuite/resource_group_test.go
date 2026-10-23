package gsuite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	directory "google.golang.org/api/admin/directory/v1"
	"google.golang.org/api/option"
)

type groupAliasTransport func(*http.Request) (*http.Response, error)

func (f groupAliasTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestResourceGroupCreateStopsOnAliasError(t *testing.T) {
	const failedAlias = "failed@omio.com"
	var aliasCalls []string
	client := &http.Client{Transport: groupAliasTransport(func(r *http.Request) (*http.Response, error) {
		status := http.StatusOK
		var payload interface{} = &directory.Group{Id: "group-id", Email: "pod-test@omio.com"}
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/groups"):
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/groups/group-id"):
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/groups/group-id/aliases"):
			var alias directory.Alias
			if err := json.NewDecoder(r.Body).Decode(&alias); err != nil {
				return nil, err
			}
			aliasCalls = append(aliasCalls, alias.Alias)
			payload = &alias
			if alias.Alias == failedAlias {
				status = http.StatusBadRequest
				payload = map[string]interface{}{"error": map[string]interface{}{
					"code": status, "message": "alias creation failed",
				}}
			}
		default:
			return nil, fmt.Errorf("unexpected Directory request: %s %s", r.Method, r.URL.Path)
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       ioutil.NopCloser(bytes.NewReader(body)),
		}, nil
	})}
	service, err := directory.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	data := schema.TestResourceDataRaw(t, resourceGroup().Schema, map[string]interface{}{
		"email": "pod-test@omio.com", "aliases": []interface{}{failedAlias, "later@omio.com"},
	})
	err = resourceGroupCreate(data, &Config{directory: service, TimeoutMinutes: 1})
	if err == nil || !strings.Contains(err.Error(), failedAlias) {
		t.Errorf("expected an error identifying %s, got %v", failedAlias, err)
	}
	if !reflect.DeepEqual(aliasCalls, []string{failedAlias}) {
		t.Errorf("alias calls = %v, want only %s", aliasCalls, failedAlias)
	}
	if data.Id() != "group-id" {
		t.Errorf("created group ID = %q, want group-id", data.Id())
	}
}

func TestResourceGroupUpdateAliases(t *testing.T) {
	const legacy = "tribe-core-xp-pod-payments@omio.com"
	const old = "old-payments@omio.com"
	const added = "pod-payments-platform@omio.com"
	const other = "second-payments@omio.com"
	tests := []struct {
		name          string
		current       []string
		desired       []string
		failOperation string
		wantCalls     []string
		wantAliases   []string
		wantError     string
	}{
		{
			name:    "add preserves standard alias",
			current: []string{legacy}, desired: []string{added, legacy},
			wantCalls: []string{"insert:" + added}, wantAliases: []string{legacy, added},
		},
		{
			name:    "replace adds before removing old alias",
			current: []string{legacy, old}, desired: []string{legacy, added},
			wantCalls: []string{"insert:" + added, "delete:" + old}, wantAliases: []string{legacy, added},
		},
		{
			name:    "clear CMS alias preserves standard alias",
			current: []string{legacy, old}, desired: []string{legacy},
			wantCalls: []string{"delete:" + old}, wantAliases: []string{legacy},
		},
		{
			name:    "metadata update and reordered aliases do not mutate aliases",
			current: []string{legacy, added}, desired: []string{added, legacy},
			wantAliases: []string{legacy, added},
		},
		{
			name:    "matching addresses are case insensitive and duplicates are not inserted",
			current: []string{legacy}, desired: []string{strings.ToUpper(legacy), added, added},
			wantCalls: []string{"insert:" + added}, wantAliases: []string{legacy, added},
		},
		{
			name:      "empty desired aliases removes all editable aliases",
			current:   []string{legacy, old},
			wantCalls: []string{"delete:" + legacy, "delete:" + old}, wantAliases: []string{},
		},
		{
			name:    "failed insert returns error without removing old aliases or trying later inserts",
			current: []string{legacy, old}, desired: []string{legacy, added, other},
			failOperation: "insert:" + added, wantError: added,
			wantCalls: []string{"insert:" + added}, wantAliases: []string{legacy, old},
		},
		{
			name:    "failed delete returns error without trying later deletes",
			current: []string{legacy, old, other}, desired: []string{legacy},
			failOperation: "delete:" + old, wantError: old,
			wantCalls: []string{"delete:" + old}, wantAliases: []string{legacy, old, other},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			aliases := append([]string{}, tt.current...)
			var calls []string
			patches := 0
			client := &http.Client{Transport: groupAliasTransport(func(r *http.Request) (*http.Response, error) {
				path := r.URL.Path
				var payload interface{}
				status := http.StatusOK
				switch {
				case r.Method == http.MethodPatch && strings.HasSuffix(path, "/groups/payments-id"):
					patches++
					payload = &directory.Group{Id: "payments-id", Email: "pod-payments@omio.com"}
				case r.Method == http.MethodGet && strings.HasSuffix(path, "/aliases"):
					items := make([]interface{}, 0, len(aliases))
					for _, alias := range aliases {
						items = append(items, map[string]string{"alias": alias})
					}
					payload = map[string]interface{}{"aliases": items}
				case r.Method == http.MethodPost && strings.HasSuffix(path, "/aliases"):
					var alias directory.Alias
					if err := json.NewDecoder(r.Body).Decode(&alias); err != nil {
						return nil, err
					}
					op := "insert:" + alias.Alias
					calls = append(calls, op)
					if op == tt.failOperation {
						status = http.StatusBadRequest
					} else {
						aliases = append(aliases, alias.Alias)
						payload = &alias
					}
				case r.Method == http.MethodDelete && strings.Contains(path, "/aliases/"):
					parts := strings.SplitN(r.URL.EscapedPath(), "/aliases/", 2)
					alias, err := url.PathUnescape(parts[1])
					if err != nil {
						return nil, err
					}
					op := "delete:" + alias
					calls = append(calls, op)
					if op == tt.failOperation {
						status = http.StatusBadRequest
					} else {
						for i, current := range aliases {
							if current == alias {
								aliases = append(aliases[:i], aliases[i+1:]...)
								break
							}
						}
						status = http.StatusNoContent
					}
				case r.Method == http.MethodGet && strings.HasSuffix(path, "/groups/payments-id"):
					payload = &directory.Group{Id: "payments-id", Email: "pod-payments@omio.com", Aliases: aliases}
				default:
					return nil, fmt.Errorf("unexpected Directory request: %s %s", r.Method, path)
				}
				if status == http.StatusBadRequest {
					payload = map[string]interface{}{"error": map[string]interface{}{
						"code": status, "message": "test alias operation failed",
					}}
				}
				body, err := json.Marshal(payload)
				if err != nil {
					return nil, err
				}
				return &http.Response{
					StatusCode: status,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       ioutil.NopCloser(bytes.NewReader(body)),
				}, nil
			})}
			service, err := directory.NewService(context.Background(), option.WithHTTPClient(client))
			if err != nil {
				t.Fatal(err)
			}
			desired := make([]interface{}, len(tt.desired))
			for i, alias := range tt.desired {
				desired[i] = alias
			}
			data := schema.TestResourceDataRaw(t, resourceGroup().Schema, map[string]interface{}{
				"email": "pod-payments@omio.com", "name": "Updated Payments Pod", "aliases": desired,
			})
			data.SetId("payments-id")
			err = resourceGroupUpdate(data, &Config{directory: service, TimeoutMinutes: 1})
			if tt.wantError == "" && err != nil {
				t.Fatalf("unexpected update error: %v", err)
			}
			if tt.wantError != "" && (err == nil || !strings.Contains(err.Error(), tt.wantError)) {
				t.Fatalf("expected error containing %q, got %v", tt.wantError, err)
			}
			if patches != 1 {
				t.Errorf("expected the group metadata update, got %d PATCH requests", patches)
			}
			if !reflect.DeepEqual(calls, tt.wantCalls) {
				t.Errorf("alias mutations = %v, want %v", calls, tt.wantCalls)
			}
			want := append([]string{}, tt.wantAliases...)
			sort.Strings(aliases)
			sort.Strings(want)
			if !reflect.DeepEqual(aliases, want) {
				t.Errorf("actual aliases = %v, want %v", aliases, want)
			}
		})
	}
}
