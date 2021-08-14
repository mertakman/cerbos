// Copyright 2021 Zenauth Ltd.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"errors"
	"io"
	"reflect"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/timestamppb"

	requestv1 "github.com/cerbos/cerbos/api/genpb/cerbos/request/v1"
	responsev1 "github.com/cerbos/cerbos/api/genpb/cerbos/response/v1"
	svcv1 "github.com/cerbos/cerbos/api/genpb/cerbos/svc/v1"
)

type AdminClient interface {
	AddOrUpdatePolicy(context.Context, *PolicySet) error
	AccessLogs(ctx context.Context, opts AuditLogOptions) (<-chan *AccessLogEntry, error)
	DecisionLogs(ctx context.Context, opts AuditLogOptions) (<-chan *DecisionLogEntry, error)
}

// NewAdminClient creates a new admin client.
// It will look for credentials in the following order:
// - Environment: CERBOS_USERNAME and CERBOS_PASSWORD
// - Netrc file (~/.netrc if an override is not defined in the NETRC environment variable)
//
// Note that Unix domain socket connections cannot fallback to netrc and require either the
// environment variables to be defined or the credentials to provided explicitly via the
// NewAdminClientWithCredentials function.
func NewAdminClient(address string, opts ...Opt) (AdminClient, error) {
	return NewAdminClientWithCredentials(address, "", "", opts...)
}

// NewAdminClientWithCredentials creates a new admin client using credentials explicitly passed as arguments.
func NewAdminClientWithCredentials(address, username, password string, opts ...Opt) (AdminClient, error) {
	// TODO: handle this in call site
	target, user, pass, err := loadBasicAuthData(osEnvironment{}, address, username, password)
	if err != nil {
		return nil, err
	}

	grpcConn, conf, err := mkConn(target, opts...)
	if err != nil {
		return nil, err
	}

	basicAuth := newBasicAuthCredentials(user, pass)
	if conf.plaintext {
		basicAuth = basicAuth.Insecure()
	}

	return &GrpcAdminClient{client: svcv1.NewCerbosAdminServiceClient(grpcConn), creds: basicAuth}, nil
}

type GrpcAdminClient struct {
	client svcv1.CerbosAdminServiceClient
	creds  credentials.PerRPCCredentials
}

func (c *GrpcAdminClient) AddOrUpdatePolicy(ctx context.Context, policies *PolicySet) error {
	if err := policies.Validate(); err != nil {
		return err
	}

	req := &requestv1.AddOrUpdatePolicyRequest{Policies: policies.policies}
	if _, err := c.client.AddOrUpdatePolicy(ctx, req, grpc.PerRPCCredentials(c.creds)); err != nil {
		return err
	}

	return nil
}

type recvFn func() (*responsev1.ListAuditLogEntriesResponse, error)

// collectLogs collects logs from the receiver function and passes to the channel
// it will return an error if the channel type is not accepted.
func collectLogs(receiver recvFn, channel interface{}) error {
	if reflect.TypeOf(channel).Kind() != reflect.Chan {
		return errors.New("no channel type provided")
	}

	var accessLogs chan *AccessLogEntry
	var decisionLogs chan *DecisionLogEntry

	ifc := reflect.ValueOf(channel).Interface()
	switch ch := ifc.(type) {
	case chan *AccessLogEntry:
		accessLogs = ch
	case chan *DecisionLogEntry:
		decisionLogs = ch
	default:
		return errors.New("could not cast to correct type of channel")
	}

	go func() {
		if accessLogs != nil {
			defer close(accessLogs)
		} else if decisionLogs != nil {
			defer close(decisionLogs)
		}

		for {
			entry, err := receiver()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return
				}

				if accessLogs != nil {
					accessLogs <- &AccessLogEntry{Err: err}
					return
				}
				decisionLogs <- &DecisionLogEntry{Err: err}
				return
			}
			if accessLogs != nil {
				accessLogs <- &AccessLogEntry{Log: entry.GetAccessLogEntry()}
				continue
			}
			decisionLogs <- &DecisionLogEntry{Log: entry.GetDecisionLogEntry()}
		}
	}()

	return nil
}

// AccessLogs returns audit logs of the access type entries.
func (c *GrpcAdminClient) AccessLogs(ctx context.Context, opts AuditLogOptions) (<-chan *AccessLogEntry, error) {
	resp, err := c.auditLogs(ctx, requestv1.ListAuditLogEntriesRequest_KIND_ACCESS, opts)
	if err != nil {
		return nil, err
	}

	entries := make(chan *AccessLogEntry)

	err = collectLogs(resp.Recv, entries)
	if err != nil {
		return nil, err
	}

	return entries, nil
}

// DecisionLogs returns decision logs of the decision type entries.
func (c *GrpcAdminClient) DecisionLogs(ctx context.Context, opts AuditLogOptions) (<-chan *DecisionLogEntry, error) {
	resp, err := c.auditLogs(ctx, requestv1.ListAuditLogEntriesRequest_KIND_DECISION, opts)
	if err != nil {
		return nil, err
	}

	entries := make(chan *DecisionLogEntry)

	err = collectLogs(resp.Recv, entries)
	if err != nil {
		return nil, err
	}

	return entries, nil
}

func (c *GrpcAdminClient) auditLogs(ctx context.Context, kind requestv1.ListAuditLogEntriesRequest_Kind, opts AuditLogOptions) (svcv1.CerbosAdminService_ListAuditLogEntriesClient, error) {
	req := &requestv1.ListAuditLogEntriesRequest{Kind: kind}

	switch {
	case opts.Tail > 0:
		req.Filter = &requestv1.ListAuditLogEntriesRequest_Tail{Tail: opts.Tail}
	case !opts.StartTime.IsZero() && !opts.EndTime.IsZero():
		req.Filter = &requestv1.ListAuditLogEntriesRequest_Between{
			Between: &requestv1.ListAuditLogEntriesRequest_TimeRange{
				Start: timestamppb.New(opts.StartTime),
				End:   timestamppb.New(opts.EndTime),
			},
		}
	case opts.Lookup != "":
		req.Filter = &requestv1.ListAuditLogEntriesRequest_Lookup{Lookup: opts.Lookup}
	}

	if err := req.Validate(); err != nil {
		return nil, err
	}

	resp, err := c.client.ListAuditLogEntries(ctx, req, grpc.PerRPCCredentials(c.creds))
	if err != nil {
		return nil, err
	}

	return resp, nil
}
