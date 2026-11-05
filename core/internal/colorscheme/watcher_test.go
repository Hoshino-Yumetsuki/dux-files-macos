package colorscheme

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUpdatePublishesOnlyValidatedChanges(t *testing.T) {
	var events []State
	watcher := New(func(topic string, data any) {
		assert.Equal(t, Topic, topic)
		events = append(events, data.(State))
	})
	dark := State{Available: true, ColorScheme: 1}
	watcher.Update(dark)
	watcher.Update(dark)
	watcher.Update(State{Available: true, ColorScheme: 3})
	assert.Equal(t, dark, watcher.State())
	assert.Equal(t, []State{dark}, events)
	light := State{Available: true, ColorScheme: 2}
	watcher.Update(light)
	assert.Equal(t, light, watcher.State())
	assert.Equal(t, []State{dark, light}, events)
}
