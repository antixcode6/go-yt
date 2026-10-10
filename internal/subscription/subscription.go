package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/antixcode6/go-yt/internal/feed"
)

// Subscription ties a channel to its cached videos.
type Subscription struct {
	ChannelID string        `json:"channel_id"`
	Title     string        `json:"title"`
	Videos    []*feed.Video `json:"videos"`
}

// Store manages the on-disk subscription list.
type Store struct {
	mu   sync.RWMutex
	path string
	subs map[string]*Subscription // keyed by channel ID
}

// LoadAll reads subscriptions from disk, returning an empty store if the file
// does not exist.
func LoadAll(path string) (*Store, error) {
	s := &Store{
		path: path,
		subs: make(map[string]*Subscription),
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}

	var list []*Subscription
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	for _, sub := range list {
		s.subs[sub.ChannelID] = sub
	}
	return s, nil
}

// Save writes the current subscription list to disk.
func (s *Store) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]*Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		list = append(list, sub)
	}

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

// Add subscribes to a channel by its YouTube channel ID.
// It immediately fetches the latest videos via the feed client.
func (s *Store) Add(ctx context.Context, fc *feed.Client, channelID string) (*Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.subs[channelID]; exists {
		return nil, fmt.Errorf("already subscribed to %s", channelID)
	}

	ch, videos, err := fc.FetchChannel(ctx, channelID)
	if err != nil {
		return nil, err
	}

	sub := &Subscription{
		ChannelID: ch.ID,
		Title:     ch.Title,
		Videos:    videos,
	}
	s.subs[channelID] = sub
	return sub, nil
} // Remove unsubscribes from a channel.

// AddLight records a subscription without fetching its videos. Useful for
// bulk imports where the per-channel network fetch would be prohibitively
// slow. The Videos slice starts empty; a later Refresh populates it.
func (s *Store) AddLight(channelID, title string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.subs[channelID]; exists {
		return false, nil
	}
	s.subs[channelID] = &Subscription{
		ChannelID: channelID,
		Title:     title,
	}
	return true, nil
}

func (s *Store) Remove(channelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.subs[channelID]; !exists {
		return fmt.Errorf("not subscribed to %s", channelID)
	}
	delete(s.subs, channelID)
	return nil
}

// List returns all subscriptions (unsorted).
func (s *Store) List() []*Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		out = append(out, sub)
	}
	return out
}

// Refresh fetches new videos for every subscription and updates the cache.
func (s *Store) Refresh(ctx context.Context, fc *feed.Client) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, sub := range s.subs {
		_, videos, err := fc.FetchChannel(ctx, id)
		if err != nil {
			// Log and continue; a single broken feed shouldn't kill the refresh.
			fmt.Fprintf(os.Stderr, "refresh %s: %v\n", id, err)
			continue
		}
		sub.Videos = videos
	}
	return nil
}
