package store

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// The hosted adapter keeps the existing SQLite-shaped queries in one place.
// PostgreSQL tables are explicitly namespaced, which also works with Neon's
// transaction pooler without relying on connection-local search_path state.
const postgresSchema = "opportunity_radar"

type database struct {
	*sql.DB
	postgres bool
}

type transaction struct {
	*sql.Tx
	db *database
}

func openPostgres(dsn string) (*Store, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection configuration")
	}
	config.ConnectTimeout = 10 * time.Second
	config.DefaultQueryExecMode = pgx.QueryExecModeExec
	if strings.HasSuffix(config.Host, ".neon.tech") {
		// Neon carries PostgreSQL's wire protocol over secure WebSockets.
		// This works on HTTPS-only networks and avoids a second TLS layer.
		host := config.Host
		config.TLSConfig = nil
		config.Fallbacks = nil
		config.ChannelBinding = "disable" // TLS is verified by the secure WebSocket transport.
		config.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) {
			connection, _, err := websocket.Dial(ctx, "wss://"+host+"/v2", nil)
			if err != nil {
				return nil, err
			}
			connection.SetReadLimit(8 << 20)
			return websocket.NetConn(context.Background(), connection, websocket.MessageBinary), nil
		}
	}
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(5 * time.Minute)
	s := &Store{db: &database{DB: db, postgres: true}}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (d *database) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.DB.ExecContext(ctx, d.statement(query), arguments(args)...)
}
func (d *database) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, d.statement(query), arguments(args)...)
}
func (d *database) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, d.statement(query), arguments(args)...)
}
func (d *database) BeginTx(ctx context.Context, options *sql.TxOptions) (*transaction, error) {
	tx, err := d.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &transaction{Tx: tx, db: d}, nil
}
func (t *transaction) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, t.db.statement(query), arguments(args)...)
}
func (t *transaction) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, t.db.statement(query), arguments(args)...)
}
func (t *transaction) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, t.db.statement(query), arguments(args)...)
}

func arguments(values []any) []any {
	result := make([]any, len(values))
	for i, value := range values {
		switch v := value.(type) {
		case bool:
			if v {
				result[i] = int64(1)
			} else {
				result[i] = int64(0)
			}
		case *int64:
			if v != nil {
				result[i] = *v
			}
		default:
			result[i] = value
		}
	}
	return result
}

func (d *database) lockRow(query string) string {
	if d.postgres {
		return query + " FOR UPDATE"
	}
	return query
}

func (d *database) statement(query string) string {
	if !d.postgres {
		return query
	}
	for _, key := range []string{"source_id", "source_kind", "source_url"} {
		query = strings.ReplaceAll(query, "json_extract(data,'$."+key+"')", "(convert_from(data,'UTF8')::jsonb ->> '"+key+"')")
	}
	query = strings.ReplaceAll(query, "ORDER BY rowid", "ORDER BY (convert_from(data,'UTF8')::jsonb ->> 'created_at'), id")
	var out strings.Builder
	bind := 0
	for i := 0; i < len(query); {
		c := query[i]
		if c == '\'' || c == '"' {
			quote, start := c, i
			i++
			for i < len(query) {
				if query[i] == quote {
					i++
					if i < len(query) && query[i] == quote {
						i++
						continue
					}
					break
				}
				i++
			}
			out.WriteString(query[start:i])
			continue
		}
		if c == '?' {
			i++
			start := i
			for i < len(query) && query[i] >= '0' && query[i] <= '9' {
				i++
			}
			index := 0
			if i > start {
				index, _ = strconv.Atoi(query[start:i])
			} else {
				bind++
				index = bind
			}
			bind = max(bind, index)
			out.WriteString("$" + strconv.Itoa(index))
			continue
		}
		if identifier(c) {
			start := i
			for i < len(query) && (identifier(query[i]) || query[i] >= '0' && query[i] <= '9') {
				i++
			}
			token := query[start:i]
			switch token {
			case "opportunities", "sources", "observations", "runs", "leases", "radar_schema", "catalog_meta", "catalog_chunks":
				out.WriteString(postgresSchema + "." + token)
			default:
				out.WriteString(token)
			}
			continue
		}
		out.WriteByte(c)
		i++
	}
	return out.String()
}

func identifier(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' }
