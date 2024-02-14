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

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type Req struct {
	Scheme, Host, Path, Method string
	Port                       int16
	Headers                    http.Header
	Body                       []byte
	CreatedAt                  time.Time
}

func (req *Req) HostAndPort() string {
	return fmt.Sprintf("%s:%d", req.Host, req.Port)
}

func (req *Req) HeadersSerialized() []byte {
	var buf bytes.Buffer
	req.Headers.Write(&buf)
	return buf.Bytes()
}

type Resp struct {
	Status    int16
	Headers   http.Header
	CreatedAt time.Time
	Body      []byte
}

const REQ_OWN = 0
const REQ_WAIT = 1

type ReqAccessor struct {
	conn  *sqlite.Conn
	req   *Req
	reqID int64
	resp  *Resp
	state uint8
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
	err = sqlitex.Execute(
		conn,
		`INSERT INTO reqs (scheme, host_and_port, path, method, headers, created_at_ms, body) VALUES (?, ?, ?, ?, ?, ?)`,
		&sqlitex.ExecOptions{
			Args: []any{req.Scheme, req.HostAndPort(), req.Path, req.Method, req.HeadersSerialized(), req.CreatedAt.UnixMilli(), req.Body},
		},
	)
	if err != nil {
		return 0, err
	}
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

		return &Resp{
			Status:    int16(respDB.Status),
			Headers:   http.Header(headers),
			Body:      respDB.Body,
			CreatedAt: time.UnixMilli(respDB.CreatedAtMs),
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
		ra.state = REQ_OWN
		return ra, nil
	} else {
		ra.reqID = reqID
		ra.req.CreatedAt = *createdAt
	}

	resp, err := getResp(conn, reqID)
	if err != nil {
		return nil, err
	}
	if resp != nil {
		ra.resp = resp
		return ra, nil
	}

	ra.state = REQ_WAIT
	return ra, nil
}

func (ra *ReqAccessor) State() uint8 {
	return ra.state
}

func (ra *ReqAccessor) Resp() *Resp {
	return ra.resp
}
