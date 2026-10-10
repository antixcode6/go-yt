package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// NewPipe and Pipepipe export a JSON file with this shape. service_id 0 is
// YouTube; other values are PeerTube, SoundCloud, etc.
type newPipeExport struct {
	AppVersion    string       `json:"app_version"`
	Subscriptions []newPipeSub `json:"subscriptions"`
}

type newPipeSub struct {
	URL        string `json:"url"`
	ChannelURL string `json:"channel_url"` // some exports use this key
	Name       string `json:"name"`
	ServiceID  *int   `json:"service_id"`
}

// ImportResult summarizes what happened during an import.
type ImportResult struct {
	Added    int
	Skipped  int // already subscribed
	Failed   int
	Failures []string
}

var channelIDRe = regexp.MustCompile(`channel/(UC[a-zA-Z0-9_-]{22})`)

// ImportNewPipe reads a NewPipe/Pipepipe subscription export and merges it
// into the store. Channels that are already subscribed are skipped without
// error; channels that fail to fetch are recorded in Failures.
//
// This is network-bound: each new channel triggers a fetch. A 100-channel
// import can take several minutes.
func ImportNewPipe(ctx context.Context, store *Store, path string) (*ImportResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var export newPipeExport
	if err := json.Unmarshal(data, &export); err != nil {
		return nil, fmt.Errorf("parse NewPipe JSON: %w", err)
	}
	if len(export.Subscriptions) == 0 {
		return nil, fmt.Errorf("no subscriptions found in %s", path)
	}

	result := &ImportResult{}
	for _, entry := range export.Subscriptions {
		if entry.ServiceID != nil && *entry.ServiceID != 0 {
			continue
		}
		rawURL := entry.URL
		if rawURL == "" {
			rawURL = entry.ChannelURL
		}
		match := channelIDRe.FindStringSubmatch(rawURL)
		if match == nil {
			result.Failed++
			continue
		}
		added, err := store.AddLight(match[1], entry.Name)
		if err != nil {
			result.Failed++
			continue
		}
		if added {
			result.Added++
		} else {
			result.Skipped++
		}
	}

	if err := store.Save(); err != nil {
		return result, fmt.Errorf("save: %w", err)
	}
	return result, nil
}
