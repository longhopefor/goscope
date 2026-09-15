// Package session stores versioned conversation snapshots, not durable tool transactions.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/longhopefor/goscope/msg"
	"github.com/longhopefor/goscope/tool"
)

var (
	ErrNotFound = errors.New("session not found")
	ErrConflict = errors.New("session version conflict")
	ErrBlocked  = errors.New("session requires manual resolution")
)

type Key struct {
	Namespace string
	ID        string
}

func (k Key) Validate() error {
	if strings.TrimSpace(k.Namespace) == "" || strings.TrimSpace(k.ID) == "" {
		return fmt.Errorf("session namespace and ID required")
	}
	return nil
}

type State string

const (
	Ready   State = "ready"
	Blocked State = "blocked"
)

// Version is an optimistic revision; FormatVersion describes the serialized schema.
type Snapshot struct {
	FormatVersion int
	Key           Key
	Version       int64
	State         State
	History       []*msg.Msg
	LastRunID     string
	StopReason    string
	ToolBatches   []*tool.BatchResult
}

func New(key Key) *Snapshot { return &Snapshot{FormatVersion: 1, Key: key, State: Ready} }

// Save creates at Version 0, otherwise atomically replaces only that revision.
// Success returns an independent snapshot at Version+1. Never retry a run on conflict.
type Store interface {
	Load(context.Context, Key) (*Snapshot, error)
	Save(context.Context, *Snapshot) (*Snapshot, error)
}

func (s *Snapshot) Validate() error {
	if s == nil {
		return fmt.Errorf("nil session")
	}
	if err := s.Key.Validate(); err != nil {
		return err
	}
	if s.FormatVersion != 1 {
		return fmt.Errorf("unsupported session format %d", s.FormatVersion)
	}
	if s.Version < 0 {
		return fmt.Errorf("invalid session version")
	}
	if s.State != Ready && s.State != Blocked {
		return fmt.Errorf("invalid session state")
	}
	if err := msg.ValidateConversation(s.History, s.State == Ready); err != nil {
		return err
	}
	for _, b := range s.ToolBatches {
		if b == nil {
			return fmt.Errorf("nil tool batch")
		}
		if b.Message != nil {
			if err := b.Message.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}
func Clone(s *Snapshot) (*Snapshot, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return Decode(raw)
}
func Decode(raw []byte) (*Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}
