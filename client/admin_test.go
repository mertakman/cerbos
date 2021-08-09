// Copyright 2021 Zenauth Ltd.
// SPDX-License-Identifier: Apache-2.0

package client_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cerbos/cerbos/client"
	"github.com/cerbos/cerbos/client/testutil"
	"github.com/cerbos/cerbos/internal/audit"
	"github.com/cerbos/cerbos/internal/audit/local"
)

func TestAuditLogs(t *testing.T) {
	serverOpts := mkServerOpts(t, false)
	tempDir := t.TempDir()
	serverOpts = append(serverOpts,
		testutil.WithHTTPListenAddr(fmt.Sprintf("unix:%s", filepath.Join(tempDir, "http.sock"))),
		testutil.WithGRPCListenAddr(fmt.Sprintf("unix:%s", filepath.Join(tempDir, "grpc.sock"))),
	)
	s, err := testutil.StartCerbosServer(serverOpts...)
	require.NoError(t, err)

	audit.RegisterBackend("local", func(_ context.Context) (audit.Log, error) {
		return local.New()
	})

	defer s.Stop() //nolint:errcheck

	ac, err := client.NewAdminClientWithCredentials(s.GRPCAddr(), adminUsername, adminPassword, client.WithPlaintext())
	require.NoError(t, err)

	loadPolicies(t, ac)

	decisionLogs, err := ac.DecisionLogs(context.Background(), client.AuditLogOptions{
		StartTime: time.Now().Add(time.Duration(-10) * time.Minute),
		EndTime:   time.Now(),
	})
	require.NoError(t, err)
	defaultFlushInterval := 30 * time.Second

	select {
	case log, ok := <-decisionLogs:
		if ok {
			require.NoError(t, log.Err)
		}
	case <-time.After(defaultFlushInterval):
		require.Fail(t, "timeout waiting for logs")
	}

	accessLogs, err := ac.AccessLogs(context.Background(), client.AuditLogOptions{
		StartTime: time.Now().Add(time.Duration(-10) * time.Minute),
		EndTime:   time.Now(),
	})
	require.NoError(t, err)

	select {
	case log, ok := <-accessLogs:
		if ok {
			require.NoError(t, log.Err)
		}
	case <-time.After(defaultFlushInterval):
		require.Fail(t, "timeout waiting for logs")
	}
}
