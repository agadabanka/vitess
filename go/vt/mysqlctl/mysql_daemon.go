// Copyright 2012, Google Inc. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package mysqlctl

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	mproto "github.com/youtube/vitess/go/mysql/proto"
	"github.com/youtube/vitess/go/sqldb"
	"github.com/youtube/vitess/go/stats"
	"github.com/youtube/vitess/go/vt/dbconnpool"
	"github.com/youtube/vitess/go/vt/mysqlctl/proto"
	"golang.org/x/net/context"
)

// MysqlDaemon is the interface we use for abstracting Mysqld.
type MysqlDaemon interface {
	// Cnf returns the underlying mycnf
	Cnf() *Mycnf

	// methods related to mysql running or not
	Start(ctx context.Context) error
	Shutdown(ctx context.Context, waitForMysqld bool) error
	RunMysqlUpgrade() error

	// GetMysqlPort returns the current port mysql is listening on.
	GetMysqlPort() (int, error)

	// replication related methods
	SlaveStatus() (proto.ReplicationStatus, error)

	// reparenting related methods
	ResetReplicationCommands() ([]string, error)
	MasterPosition() (proto.ReplicationPosition, error)
	IsReadOnly() (bool, error)
	SetReadOnly(on bool) error
	StartReplicationCommands(status *proto.ReplicationStatus) ([]string, error)
	SetMasterCommands(masterHost string, masterPort int) ([]string, error)
	WaitForReparentJournal(ctx context.Context, timeCreatedNS int64) error

	// DemoteMaster waits for all current transactions to finish,
	// and returns the current replication position. It will not
	// change the read_only state of the server.
	DemoteMaster() (proto.ReplicationPosition, error)

	WaitMasterPos(proto.ReplicationPosition, time.Duration) error

	// PromoteSlave makes the slave the new master. It will not change
	// the read_only state of the server.
	PromoteSlave(map[string]string) (proto.ReplicationPosition, error)

	// Schema related methods
	GetSchema(dbName string, tables, excludeTables []string, includeViews bool) (*proto.SchemaDefinition, error)
	PreflightSchemaChange(dbName string, change string) (*proto.SchemaChangeResult, error)
	ApplySchemaChange(dbName string, change *proto.SchemaChange) (*proto.SchemaChangeResult, error)

	// GetAppConnection returns a app connection to be able to talk to the database.
	GetAppConnection() (dbconnpool.PoolConnection, error)
	// GetDbaConnection returns a dba connection.
	GetDbaConnection() (*dbconnpool.DBConnection, error)

	// ExecuteSuperQueryList executes a list of queries, no result
	ExecuteSuperQueryList(queryList []string) error

	// FetchSuperQuery executes one query, returns the result
	FetchSuperQuery(query string) (*mproto.QueryResult, error)

	// NewSlaveConnection returns a SlaveConnection to the database.
	NewSlaveConnection() (*SlaveConnection, error)

	// EnableBinlogPlayback enables playback of binlog events
	EnableBinlogPlayback() error

	// DisableBinlogPlayback disable playback of binlog events
	DisableBinlogPlayback() error

	// Close will close this instance of Mysqld. It will wait for all dba
	// queries to be finished.
	Close()
}

// FakeMysqlDaemon implements MysqlDaemon and allows the user to fake
// everything.
type FakeMysqlDaemon struct {
	// Mycnf will be returned by Cnf()
	Mycnf *Mycnf

	// Running is used by Start / Shutdown
	Running bool

	// MysqlPort will be returned by GetMysqlPort(). Set to -1 to
	// return an error.
	MysqlPort int

	// Replicating is updated when calling StartSlave / StopSlave
	// (it is not used at all when calling SlaveStatus, it is the
	// test owner responsability to have these two match)
	Replicating bool

	// ResetReplicationResult is returned by ResetReplication
	ResetReplicationResult []string

	// ResetReplicationError is returned by ResetReplication
	ResetReplicationError error

	// CurrentMasterPosition is returned by MasterPosition
	// and SlaveStatus
	CurrentMasterPosition proto.ReplicationPosition

	// CurrentMasterHost is returned by SlaveStatus
	CurrentMasterHost string

	// CurrentMasterport is returned by SlaveStatus
	CurrentMasterPort int

	// ReadOnly is the current value of the flag
	ReadOnly bool

	// StartReplicationCommandsStatus is matched against the input
	// of StartReplicationCommands. If it doesn't match,
	// StartReplicationCommands will return an error.
	StartReplicationCommandsStatus *proto.ReplicationStatus

	// StartReplicationCommandsResult is what
	// StartReplicationCommands will return
	StartReplicationCommandsResult []string

	// SetMasterCommandsInput is matched against the input
	// of SetMasterCommands (as "%v:%v"). If it doesn't match,
	// SetMasterCommands will return an error.
	SetMasterCommandsInput string

	// SetMasterCommandsResult is what
	// SetMasterCommands will return
	SetMasterCommandsResult []string

	// DemoteMasterPosition is returned by DemoteMaster
	DemoteMasterPosition proto.ReplicationPosition

	// WaitMasterPosition is checked by WaitMasterPos, if the
	// same it returns nil, if different it returns an error
	WaitMasterPosition proto.ReplicationPosition

	// PromoteSlaveResult is returned by PromoteSlave
	PromoteSlaveResult proto.ReplicationPosition

	// Schema will be returned by GetSchema. If nil we'll
	// return an error.
	Schema *proto.SchemaDefinition

	// PreflightSchemaChangeResult will be returned by PreflightSchemaChange.
	// If nil we'll return an error.
	PreflightSchemaChangeResult *proto.SchemaChangeResult

	// ApplySchemaChangeResult will be returned by ApplySchemaChange.
	// If nil we'll return an error.
	ApplySchemaChangeResult *proto.SchemaChangeResult

	// DbAppConnectionFactory is the factory for making fake db app connections
	DbAppConnectionFactory func() (dbconnpool.PoolConnection, error)

	// ExpectedExecuteSuperQueryList is what we expect
	// ExecuteSuperQueryList to be called with. If it doesn't
	// match, ExecuteSuperQueryList will return an error.
	// Note each string is just a substring if it begins with SUB,
	// so we support partial queries (usefull when queries contain
	// data fields like timestamps)
	ExpectedExecuteSuperQueryList []string

	// ExpectedExecuteSuperQueryCurrent is the current index of the queries
	// we expect
	ExpectedExecuteSuperQueryCurrent int

	// FetchSuperQueryResults is used by FetchSuperQuery
	FetchSuperQueryMap map[string]*mproto.QueryResult

	// BinlogPlayerEnabled is used by {Enable,Disable}BinlogPlayer
	BinlogPlayerEnabled bool
}

// NewFakeMysqlDaemon returns a FakeMysqlDaemon where mysqld appears
// to be running
func NewFakeMysqlDaemon() *FakeMysqlDaemon {
	return &FakeMysqlDaemon{
		Running: true,
	}
}

// Cnf is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) Cnf() *Mycnf {
	return fmd.Mycnf
}

// Start is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) Start(ctx context.Context) error {
	if fmd.Running {
		return fmt.Errorf("fake mysql daemon already running")
	}
	fmd.Running = true
	return nil
}

// Shutdown is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) Shutdown(ctx context.Context, waitForMysqld bool) error {
	if !fmd.Running {
		return fmt.Errorf("fake mysql daemon not running")
	}
	fmd.Running = false
	return nil
}

// RunMysqlUpgrade is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) RunMysqlUpgrade() error {
	return nil
}

// GetMysqlPort is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) GetMysqlPort() (int, error) {
	if fmd.MysqlPort == -1 {
		return 0, fmt.Errorf("FakeMysqlDaemon.GetMysqlPort returns an error")
	}
	return fmd.MysqlPort, nil
}

// SlaveStatus is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) SlaveStatus() (proto.ReplicationStatus, error) {
	return proto.ReplicationStatus{
		Position:        fmd.CurrentMasterPosition,
		SlaveIORunning:  fmd.Replicating,
		SlaveSQLRunning: fmd.Replicating,
		MasterHost:      fmd.CurrentMasterHost,
		MasterPort:      fmd.CurrentMasterPort,
	}, nil
}

// ResetReplicationCommands is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) ResetReplicationCommands() ([]string, error) {
	return fmd.ResetReplicationResult, fmd.ResetReplicationError
}

// MasterPosition is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) MasterPosition() (proto.ReplicationPosition, error) {
	return fmd.CurrentMasterPosition, nil
}

// IsReadOnly is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) IsReadOnly() (bool, error) {
	return fmd.ReadOnly, nil
}

// SetReadOnly is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) SetReadOnly(on bool) error {
	fmd.ReadOnly = on
	return nil
}

// StartReplicationCommands is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) StartReplicationCommands(status *proto.ReplicationStatus) ([]string, error) {
	status.MasterConnectRetry = int(masterConnectRetry.Seconds())
	if !reflect.DeepEqual(fmd.StartReplicationCommandsStatus, status) {
		return nil, fmt.Errorf("wrong status for StartReplicationCommands: expected %v got %v", fmd.StartReplicationCommandsStatus, status)
	}
	return fmd.StartReplicationCommandsResult, nil
}

// SetMasterCommands is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) SetMasterCommands(masterHost string, masterPort int) ([]string, error) {
	input := fmt.Sprintf("%v:%v", masterHost, masterPort)
	if fmd.SetMasterCommandsInput != input {
		return nil, fmt.Errorf("wrong input for SetMasterCommands: expected %v got %v", fmd.SetMasterCommandsInput, input)
	}
	return fmd.SetMasterCommandsResult, nil
}

// WaitForReparentJournal is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) WaitForReparentJournal(ctx context.Context, timeCreatedNS int64) error {
	return nil
}

// DemoteMaster is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) DemoteMaster() (proto.ReplicationPosition, error) {
	return fmd.DemoteMasterPosition, nil
}

// WaitMasterPos is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) WaitMasterPos(pos proto.ReplicationPosition, waitTimeout time.Duration) error {
	if reflect.DeepEqual(fmd.WaitMasterPosition, pos) {
		return nil
	}
	return fmt.Errorf("wrong input for WaitMasterPos: expected %v got %v", fmd.WaitMasterPosition, pos)
}

// PromoteSlave is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) PromoteSlave(hookExtraEnv map[string]string) (proto.ReplicationPosition, error) {
	return fmd.PromoteSlaveResult, nil
}

// ExecuteSuperQueryList is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) ExecuteSuperQueryList(queryList []string) error {
	for _, query := range queryList {
		// test we still have a query to compare
		if fmd.ExpectedExecuteSuperQueryCurrent >= len(fmd.ExpectedExecuteSuperQueryList) {
			return fmt.Errorf("unexpected extra query in ExecuteSuperQueryList: %v", query)
		}

		// compare the query
		expected := fmd.ExpectedExecuteSuperQueryList[fmd.ExpectedExecuteSuperQueryCurrent]
		fmd.ExpectedExecuteSuperQueryCurrent++
		if strings.HasPrefix(expected, "SUB") {
			// remove the SUB from the expected,
			// and truncate the query to length(expected)
			expected = expected[3:]
			if len(query) > len(expected) {
				query = query[:len(expected)]
			}
		}
		if expected != query {
			return fmt.Errorf("wrong query for ExecuteSuperQueryList: expected %v got %v", expected, query)
		}

		// intercept some queries to update our status
		switch query {
		case SqlStartSlave:
			fmd.Replicating = true
		case SqlStopSlave:
			fmd.Replicating = false
		}
	}
	return nil
}

// FetchSuperQuery returns the results from the map, if any
func (fmd *FakeMysqlDaemon) FetchSuperQuery(query string) (*mproto.QueryResult, error) {
	if fmd.FetchSuperQueryMap == nil {
		return nil, fmt.Errorf("unexpected query: %v", query)
	}

	qr, ok := fmd.FetchSuperQueryMap[query]
	if !ok {
		return nil, fmt.Errorf("unexpected query: %v", query)
	}
	return qr, nil
}

// NewSlaveConnection is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) NewSlaveConnection() (*SlaveConnection, error) {
	panic(fmt.Errorf("not implemented on FakeMysqlDaemon"))
}

// EnableBinlogPlayback is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) EnableBinlogPlayback() error {
	if fmd.BinlogPlayerEnabled {
		return fmt.Errorf("binlog player already enabled")
	}
	fmd.BinlogPlayerEnabled = true
	return nil
}

// DisableBinlogPlayback disable playback of binlog events
func (fmd *FakeMysqlDaemon) DisableBinlogPlayback() error {
	if fmd.BinlogPlayerEnabled {
		return fmt.Errorf("binlog player already disabled")
	}
	fmd.BinlogPlayerEnabled = false
	return nil
}

// Close is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) Close() {
}

// CheckSuperQueryList returns an error if all the queries we expected
// haven't been seen.
func (fmd *FakeMysqlDaemon) CheckSuperQueryList() error {
	if fmd.ExpectedExecuteSuperQueryCurrent != len(fmd.ExpectedExecuteSuperQueryList) {
		return fmt.Errorf("SuperQueryList wasn't consumed, saw %v queries, was expecting %v", fmd.ExpectedExecuteSuperQueryCurrent, len(fmd.ExpectedExecuteSuperQueryList))
	}
	return nil
}

// GetSchema is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) GetSchema(dbName string, tables, excludeTables []string, includeViews bool) (*proto.SchemaDefinition, error) {
	if fmd.Schema == nil {
		return nil, fmt.Errorf("no schema defined")
	}
	return fmd.Schema.FilterTables(tables, excludeTables, includeViews)
}

// PreflightSchemaChange is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) PreflightSchemaChange(dbName string, change string) (*proto.SchemaChangeResult, error) {
	if fmd.PreflightSchemaChangeResult == nil {
		return nil, fmt.Errorf("no preflight result defined")
	}
	return fmd.PreflightSchemaChangeResult, nil
}

// ApplySchemaChange is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) ApplySchemaChange(dbName string, change *proto.SchemaChange) (*proto.SchemaChangeResult, error) {
	if fmd.ApplySchemaChangeResult == nil {
		return nil, fmt.Errorf("no apply schema defined")
	}
	return fmd.ApplySchemaChangeResult, nil
}

// GetAppConnection is part of the MysqlDaemon interface
func (fmd *FakeMysqlDaemon) GetAppConnection() (dbconnpool.PoolConnection, error) {
	if fmd.DbAppConnectionFactory == nil {
		return nil, fmt.Errorf("no DbAppConnectionFactory set in this FakeMysqlDaemon")
	}
	return fmd.DbAppConnectionFactory()
}

// GetDbaConnection is part of the MysqlDaemon interface.
func (fmd *FakeMysqlDaemon) GetDbaConnection() (*dbconnpool.DBConnection, error) {
	return dbconnpool.NewDBConnection(&sqldb.ConnParams{}, stats.NewTimings(""))
}
