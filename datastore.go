package slackoncallbot

import (
	"golang.org/x/net/context"
	"google.golang.org/appengine/datastore"
	"sort"
)

// func loadState {{{

// At start up, load all existing state from datastore.
func loadState(ctx context.Context) error {
	// Get list of teams we support from datastore.
	q := datastore.NewQuery(oncallKind)
	oncallMut.Lock()
	defer oncallMut.Unlock()
	if _, err := q.GetAll(ctx, &rotations); err != nil {
		return err
	}
	sort.Sort(rotations)
	log.Infof(ctx, "loaded previous on-call states, %d entries loaded", len(rotations))
	return nil
} // }}}

// func saveState {{{

// Save current oncall rotation state in DataStore.
func saveState(ctx context.Context, entity *oncallProperty) error {
	// The "key" is the team name.
	// If this is an existing entry then the "key" should be there.
	// If not, create one and save it.
	var err error
	if entity.Key == nil {
		entity.Key = datastore.NewKey(ctx, oncallKind, entity.Team, 0, nil)
	}

	// Save the new entry and return.
	if _, err = datastore.Put(ctx, entity.Key, entity); err != nil {
		return err
	}

	return nil
} // }}}

// func deleteState {{{

// Delete requested key from datastore.
func deleteState(ctx context.Context, key *datastore.Key) error {
	return datastore.Delete(ctx, key)
} // }}}
