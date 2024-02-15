package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/avast/retry-go/v4"
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type Req struct {
	Scheme, Host, Path, Method string
	Port                       *string
	Headers                    http.Header
	Body                       []byte
	CreatedAt                  *time.Time
}

func (req *Req) HostAndPort() string {
	hostAndPort := req.Host
	if req.Port != nil {
		hostAndPort = fmt.Sprintf("%s:%s", hostAndPort, *req.Port)
	}
	return hostAndPort
}

func (req *Req) HeadersSerialized() []byte {
	var buf bytes.Buffer
	req.Headers.Write(&buf)
	return buf.Bytes()
}

type Resp struct {
	Status    int32
	Headers   http.Header
	CreatedAt *time.Time
	Body      []byte
}

func (resp *Resp) HeadersSerialized() []byte {
	var buf bytes.Buffer
	resp.Headers.Write(&buf)
	return buf.Bytes()
}

type ReqAccessor struct {
	conn  *sqlite.Conn
	req   *Req
	reqID int64
	resp  *Resp
	owns  bool
}

func getReq(
	conn *sqlite.Conn, req *Req,
) (reqID int64, createdAt *time.Time, err error) {
	err = sqlitex.Execute(
		conn,
		`SELECT id, created_at_ms FROM reqs WHERE scheme = ? AND host_and_port = ? AND path = ? AND method = ? AND headers = ?`,
		&sqlitex.ExecOptions{
			ResultFunc: func(stmt *sqlite.Stmt) error {
				reqID = stmt.ColumnInt64(0)
				at := time.UnixMilli(stmt.ColumnInt64(1))
				createdAt = &at
				return nil
			},
			Args: []any{req.Scheme, req.HostAndPort(), req.Path, req.Method, req.HeadersSerialized()},
		},
	)
	return reqID, createdAt, err
}

func insertReq(
	conn *sqlite.Conn, req *Req,
) (reqID int64, err error) {
	now := time.Now()
	err = sqlitex.Execute(
		conn,
		`INSERT INTO reqs (scheme, host_and_port, path, method, headers, created_at_ms) VALUES (?, ?, ?, ?, ?, ?)`,
		&sqlitex.ExecOptions{
			Args: []any{
				req.Scheme,
				req.HostAndPort(),
				req.Path,
				req.Method,
				req.HeadersSerialized(),
				now.UnixMilli(),
			},
		},
	)
	if err != nil {
		return 0, err
	}
	req.CreatedAt = &now
	return conn.LastInsertRowID(), nil
}

func getResp(conn *sqlite.Conn, reqID int64) (*Resp, error) {
	type RespDB struct {
		Status      int64
		Headers     string
		Body        []byte
		CreatedAtMs int64
	}
	var respDB *RespDB
	err := sqlitex.Execute(
		conn,
		`SELECT status, headers, created_at_ms, body FROM resps WHERE req_id = ?`,
		&sqlitex.ExecOptions{
			ResultFunc: func(stmt *sqlite.Stmt) error {
				bodyLen := stmt.ColumnLen(3)
				bodySlice := make([]byte, bodyLen)
				stmt.ColumnBytes(3, bodySlice)
				respDB = &RespDB{
					Status:      stmt.ColumnInt64(0),
					Headers:     stmt.ColumnText(1),
					Body:        bodySlice,
					CreatedAtMs: stmt.ColumnInt64(2),
				}
				return nil
			},
			Args: []any{reqID},
		},
	)
	if err != nil {
		return nil, err
	}
	if respDB != nil {
		reader := textproto.NewReader(
			bufio.NewReader(
				strings.NewReader(respDB.Headers)))
		headers, err := reader.ReadMIMEHeader()
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}

		createdAt := time.UnixMilli(respDB.CreatedAtMs)
		return &Resp{
			Status:    int32(respDB.Status),
			Headers:   http.Header(headers),
			Body:      respDB.Body,
			CreatedAt: &createdAt,
		}, nil
	}
	return nil, nil
}

func NewReqAccessor(conn *sqlite.Conn, req *Req) (*ReqAccessor, error) {
	ra := &ReqAccessor{conn: conn, req: req}
	reqID, createdAt, err := getReq(conn, req)
	if err != nil {
		return nil, err
	}
	if reqID == 0 {
		insertedReqID, err := insertReq(conn, req)
		if err != nil {
			// TODO: HANDLE RACE
			// between multiple inserters
			log.Fatal(err)
		}
		log.Printf("insertedReqID: %v\n", insertedReqID)
		ra.reqID = insertedReqID
		ra.owns = true
		return ra, nil
	} else {
		ra.reqID = reqID
		ra.req.CreatedAt = createdAt
	}

	resp, err := getResp(conn, reqID)
	if err != nil {
		return nil, err
	}
	if resp != nil {
		ra.resp = resp
		return ra, nil
	}

	return ra, nil
}

func (ra *ReqAccessor) Resp() (*Resp, error) {
	// TODO Blocks if the state is REQ_WAIT
	if ra.resp != nil {
		return ra.resp, nil
	}
	if ra.owns {
		return nil, nil
	}

	// another goroutine is fetching the response
	// looping wait for it and return
	err := retry.Do(
		func() error {
			resp, err := getResp(ra.conn, ra.reqID)
			if err != nil {
				return err
			}
		},
	)

	return nil, err
}

func (ra *ReqAccessor) SetReqBody(conn *sqlite.Conn, body []byte) error {
	if len(body) == 0 {
		return nil
	}
	if ra.reqID == 0 {
		return errors.New("reqID is not set")
	}

	err := sqlitex.Execute(
		conn,
		`UPDATE reqs SET body = ? WHERE id = ?`,
		&sqlitex.ExecOptions{
			Args: []any{body, ra.reqID},
		},
	)
	if err != nil {
		return err
	}
	ra.req.Body = body
	return nil
}

func (ra *ReqAccessor) SetResp(conn *sqlite.Conn, resp *Resp) error {
	if ra.reqID == 0 {
		return errors.New("reqID is not set")
	}

	if resp.CreatedAt == nil {
		now := time.Now()
		resp.CreatedAt = &now
	}

	err := sqlitex.Execute(
		conn,
		`INSERT INTO resps (req_id, status, headers, body, created_at_ms) VALUES (?, ?, ?, ?, ?)`,
		&sqlitex.ExecOptions{
			Args: []any{
				ra.reqID,
				resp.Status,
				resp.HeadersSerialized(),
				resp.Body,
				resp.CreatedAt.UnixMilli(),
			},
		},
	)
	if err != nil {
		return err
	}
	ra.resp = resp
	return nil
}
