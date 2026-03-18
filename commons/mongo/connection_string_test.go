//go:build unit

package mongo

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildURI_SuccessCases(t *testing.T) {
	t.Parallel()

	t.Run("mongodb with auth, port, database and query", func(t *testing.T) {
		t.Parallel()

		query := url.Values{}
		query.Set("authSource", "admin")
		query.Set("replicaSet", "rs0")

		uri, err := BuildURI(URIConfig{
			Scheme:   "mongodb",
			Username: "dbuser",
			Password: "p@ss:word/123",
			Host:     "localhost",
			Port:     "27017",
			Database: "ledger",
			Query:    query,
		})
		require.NoError(t, err)
		assert.Equal(t, "mongodb://dbuser:p%40ss%3Aword%2F123@localhost:27017/ledger?authSource=admin&replicaSet=rs0", uri)
	})

	t.Run("mongodb+srv omits port", func(t *testing.T) {
		t.Parallel()

		query := url.Values{}
		query.Set("retryWrites", "true")
		query.Set("w", "majority")

		uri, err := BuildURI(URIConfig{
			Scheme:   "mongodb+srv",
			Username: "user",
			Password: "secret",
			Host:     "cluster.mongodb.net",
			Database: "banking",
			Query:    query,
		})
		require.NoError(t, err)
		assert.Contains(t, uri, "mongodb+srv://user:secret@cluster.mongodb.net/banking?")
		assert.Contains(t, uri, "authSource=admin")
		assert.Contains(t, uri, "retryWrites=true")
		assert.Contains(t, uri, "w=majority")
	})

	t.Run("without credentials defaults to root path", func(t *testing.T) {
		t.Parallel()

		uri, err := BuildURI(URIConfig{
			Scheme: "mongodb",
			Host:   "127.0.0.1",
			Port:   "27017",
		})
		require.NoError(t, err)
		assert.Equal(t, "mongodb://127.0.0.1:27017/", uri)
	})

	t.Run("username without password", func(t *testing.T) {
		t.Parallel()

		uri, err := BuildURI(URIConfig{
			Scheme:   "mongodb",
			Username: "readonly",
			Host:     "localhost",
			Port:     "27017",
		})
		require.NoError(t, err)
		// Uses url.User (not url.UserPassword) so no trailing colon before @.
		assert.Equal(t, "mongodb://readonly@localhost:27017/", uri)
	})

	t.Run("default authSource=admin when database and credentials set", func(t *testing.T) {
		t.Parallel()

		uri, err := BuildURI(URIConfig{
			Scheme:   "mongodb",
			Username: "appuser",
			Password: "secret",
			Host:     "localhost",
			Port:     "27017",
			Database: "midaz",
		})
		require.NoError(t, err)
		assert.Equal(t, "mongodb://appuser:secret@localhost:27017/midaz?authSource=admin", uri)
	})

	t.Run("explicit authSource not overridden", func(t *testing.T) {
		t.Parallel()

		query := url.Values{}
		query.Set("authSource", "myauthdb")

		uri, err := BuildURI(URIConfig{
			Scheme:   "mongodb",
			Username: "appuser",
			Password: "secret",
			Host:     "localhost",
			Port:     "27017",
			Database: "midaz",
			Query:    query,
		})
		require.NoError(t, err)
		assert.Equal(t, "mongodb://appuser:secret@localhost:27017/midaz?authSource=myauthdb", uri)
	})

	t.Run("no authSource added without database in path", func(t *testing.T) {
		t.Parallel()

		uri, err := BuildURI(URIConfig{
			Scheme:   "mongodb",
			Username: "appuser",
			Password: "secret",
			Host:     "localhost",
			Port:     "27017",
		})
		require.NoError(t, err)
		assert.Equal(t, "mongodb://appuser:secret@localhost:27017/", uri)
		assert.NotContains(t, uri, "authSource")
	})

	t.Run("no authSource added without credentials", func(t *testing.T) {
		t.Parallel()

		uri, err := BuildURI(URIConfig{
			Scheme:   "mongodb",
			Host:     "localhost",
			Port:     "27017",
			Database: "midaz",
		})
		require.NoError(t, err)
		assert.Equal(t, "mongodb://localhost:27017/midaz", uri)
		assert.NotContains(t, uri, "authSource")
	})
}

func TestBuildURI_Validation(t *testing.T) {
	t.Parallel()

	t.Run("invalid scheme", func(t *testing.T) {
		t.Parallel()

		uri, err := BuildURI(URIConfig{Scheme: "postgres", Host: "localhost"})
		assert.Empty(t, uri)
		assert.ErrorIs(t, err, ErrInvalidScheme)
	})

	t.Run("empty host", func(t *testing.T) {
		t.Parallel()

		uri, err := BuildURI(URIConfig{Scheme: "mongodb", Host: "  "})
		assert.Empty(t, uri)
		assert.ErrorIs(t, err, ErrEmptyHost)
	})

	t.Run("invalid port", func(t *testing.T) {
		t.Parallel()

		uri, err := BuildURI(URIConfig{Scheme: "mongodb", Host: "localhost", Port: "70000"})
		assert.Empty(t, uri)
		assert.ErrorIs(t, err, ErrInvalidPort)
	})

	t.Run("srv port is forbidden", func(t *testing.T) {
		t.Parallel()

		uri, err := BuildURI(URIConfig{Scheme: "mongodb+srv", Host: "cluster.mongodb.net", Port: "27017"})
		assert.Empty(t, uri)
		assert.ErrorIs(t, err, ErrPortNotAllowedForSRV)
	})

	t.Run("password without username", func(t *testing.T) {
		t.Parallel()

		uri, err := BuildURI(URIConfig{Scheme: "mongodb", Host: "localhost", Password: "secret"})
		assert.Empty(t, uri)
		assert.ErrorIs(t, err, ErrPasswordWithoutUser)
	})

	t.Run("whitespace_only_username_treated_as_empty", func(t *testing.T) {
		t.Parallel()

		uri, err := BuildURI(URIConfig{Scheme: "mongodb", Host: "localhost", Username: "  ", Password: "secret"})
		assert.Empty(t, uri)
		assert.ErrorIs(t, err, ErrPasswordWithoutUser)
	})
}

func TestBuildURI_PortBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("port_zero_is_invalid", func(t *testing.T) {
		t.Parallel()

		_, err := BuildURI(URIConfig{Scheme: "mongodb", Host: "localhost", Port: "0"})
		assert.ErrorIs(t, err, ErrInvalidPort)
	})

	t.Run("port_one_is_valid", func(t *testing.T) {
		t.Parallel()

		uri, err := BuildURI(URIConfig{Scheme: "mongodb", Host: "localhost", Port: "1"})
		require.NoError(t, err)
		assert.Contains(t, uri, ":1/")
	})

	t.Run("port_65535_is_valid", func(t *testing.T) {
		t.Parallel()

		uri, err := BuildURI(URIConfig{Scheme: "mongodb", Host: "localhost", Port: "65535"})
		require.NoError(t, err)
		assert.Contains(t, uri, ":65535/")
	})

	t.Run("port_65536_is_invalid", func(t *testing.T) {
		t.Parallel()

		_, err := BuildURI(URIConfig{Scheme: "mongodb", Host: "localhost", Port: "65536"})
		assert.ErrorIs(t, err, ErrInvalidPort)
	})

	t.Run("non_numeric_port", func(t *testing.T) {
		t.Parallel()

		_, err := BuildURI(URIConfig{Scheme: "mongodb", Host: "localhost", Port: "abc"})
		assert.ErrorIs(t, err, ErrInvalidPort)
	})

	t.Run("negative_port", func(t *testing.T) {
		t.Parallel()

		_, err := BuildURI(URIConfig{Scheme: "mongodb", Host: "localhost", Port: "-1"})
		assert.ErrorIs(t, err, ErrInvalidPort)
	})
}
