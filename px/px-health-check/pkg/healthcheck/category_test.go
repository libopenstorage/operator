package healthcheck

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewCategories(t *testing.T) {
	c := NewCategory("test", []*Checker{}, false, "url")
	assert.NotNil(t, c)
	assert.Equal(t, c.ID, CategoryID("test"))
	assert.NotNil(t, c.Checkers)
	assert.Len(t, c.Checkers, 0)
	assert.False(t, c.Enabled)
	assert.Equal(t, c.HintBaseURL, "url")

	_, ok := c.Context.Deadline()
	assert.False(t, ok)

	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	c = NewCategoryWithContext("test2", []*Checker{}, true, "url2", ctx)
	assert.NotNil(t, c)
	assert.Equal(t, c.ID, CategoryID("test2"))
	assert.NotNil(t, c.Checkers)
	assert.Len(t, c.Checkers, 0)
	assert.True(t, c.Enabled)
	assert.Equal(t, c.HintBaseURL, "url2")

	_, ok = c.Context.Deadline()
	assert.True(t, ok)
}

func TestCategorySetContext(t *testing.T) {

	var c *Category

	assert.Nil(t, c.WithContext(context.Background()))

	c = NewCategory("test", []*Checker{}, false, "url")
	assert.NotNil(t, c)
	assert.Equal(t, c.ID, CategoryID("test"))
	assert.NotNil(t, c.Checkers)
	assert.Len(t, c.Checkers, 0)
	assert.False(t, c.Enabled)
	assert.Equal(t, c.HintBaseURL, "url")

	_, ok := c.Context.Deadline()
	assert.False(t, ok)

	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	c.WithContext(ctx)
	_, ok = c.Context.Deadline()
	assert.True(t, ok)
}
