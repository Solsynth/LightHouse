// Package discovery registers Stargate with Blade's service discovery
// (DyServiceDiscoveryService gRPC), mirroring the C# fleet's
// BladeServiceRegistrationService. Blade probes the registered HTTP endpoint
// for health and then aggregates this instance's DyCapabilitiesService into
// its /meta document — without registration, the Padlock-family capabilities
// (auth.*, e2ee, permissions, admin.*, accounts.*) vanish from /meta.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	gen "src.solsynth.dev/sosys/go/proto"
)

// Options mirrors BladeServiceDiscoveryOptions.
type Options struct {
	Service           string
	InstanceID        string
	HttpEndpoint      string
	GrpcEndpoint      string
	RegistrationToken string
	LeaseSeconds      int
	Weight            int
}

// Registration owns the register -> renew -> deregister lifecycle.
type Registration struct {
	client gen.DyServiceDiscoveryServiceClient
	opts   Options
	log    *slog.Logger

	registered bool
}

// New wires a registration over an established client connection.
func New(client gen.DyServiceDiscoveryServiceClient, opts Options, log *slog.Logger) *Registration {
	return &Registration{client: client, opts: opts, log: log}
}

// Run registers the instance and keeps the lease renewed until ctx is
// cancelled (deregistering on shutdown). Register failures retry with
// exponential backoff (5s -> 30s, like the C# retry delay).
func (r *Registration) Run(ctx context.Context) {
	retryDelay := 5 * time.Second
	for {
		interval, err := r.register(ctx)
		if err == nil {
			r.registered = true
			retryDelay = 5 * time.Second
			r.renewLoop(ctx, interval)
			if ctx.Err() != nil {
				return
			}
			// Renewal failed: fall through and re-register.
		} else if ctx.Err() != nil {
			return
		} else {
			r.registered = false
			r.log.Warn("blade service discovery registration failed",
				"service", r.opts.Service, "instance_id", r.opts.InstanceID,
				"retry_in", retryDelay.String(), "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryDelay):
		}
		retryDelay = time.Duration(math.Min(float64(retryDelay)*2, 30_000_000_000))
	}
}

// register mirrors BladeServiceRegistrationService.RegisterAsync.
func (r *Registration) register(ctx context.Context) (time.Duration, error) {
	instance := &gen.DyServiceInstance{
		Service:      r.opts.Service,
		InstanceId:   r.opts.InstanceID,
		HttpEndpoint: r.opts.HttpEndpoint,
		GrpcEndpoint: r.opts.GrpcEndpoint,
		Weight:       int32(r.opts.Weight),
	}
	response, err := r.client.Register(r.authorized(ctx), &gen.DyRegisterServiceInstanceRequest{
		Instance:     instance,
		LeaseSeconds: int32(r.opts.LeaseSeconds),
	})
	if err != nil {
		return 0, err
	}
	r.log.Info("registered with blade service discovery",
		"service", r.opts.Service, "instance_id", r.opts.InstanceID)
	return renewalInterval(response.GetLeaseExpiresAtUnixMs(), r.opts.LeaseSeconds), nil
}

// renewLoop renews the lease until it fails or the context is cancelled.
func (r *Registration) renewLoop(ctx context.Context, interval time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		response, err := r.client.Renew(r.authorized(ctx), &gen.DyRenewServiceLeaseRequest{
			Service:      r.opts.Service,
			InstanceId:   r.opts.InstanceID,
			LeaseSeconds: int32(r.opts.LeaseSeconds),
		})
		if err != nil {
			// A missing lease (e.g. Blade restarted) means re-register.
			if status.Code(err) == codes.NotFound {
				r.log.Info("blade discovery lease not found; re-registering",
					"service", r.opts.Service, "instance_id", r.opts.InstanceID)
			} else {
				r.log.Warn("blade discovery lease renewal failed",
					"service", r.opts.Service, "instance_id", r.opts.InstanceID, "error", err)
			}
			return
		}
		interval = renewalInterval(response.GetLeaseExpiresAtUnixMs(), r.opts.LeaseSeconds)
	}
}

// Deregister removes the instance from the registry (best-effort, mirrors the
// C# StopAsync).
func (r *Registration) Deregister(ctx context.Context) {
	if !r.registered {
		return
	}
	_, err := r.client.Deregister(r.authorized(ctx), &gen.DyDeregisterServiceInstanceRequest{
		Service:    r.opts.Service,
		InstanceId: r.opts.InstanceID,
	})
	if err != nil {
		r.log.Warn("blade service discovery deregistration failed",
			"service", r.opts.Service, "instance_id", r.opts.InstanceID, "error", err)
		return
	}
	r.registered = false
}

func (r *Registration) authorized(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+r.opts.RegistrationToken)
}

// renewalInterval mirrors BladeServiceRegistrationService.GetRenewalInterval:
// a third of the remaining lease (or a third of the lease when unknown),
// never below one second.
func renewalInterval(leaseExpiresAtUnixMs int64, leaseSeconds int) time.Duration {
	if leaseExpiresAtUnixMs > 0 {
		remaining := time.Until(time.UnixMilli(leaseExpiresAtUnixMs))
		if remaining > 0 {
			secs := remaining.Seconds() / 3
			if secs < 1 {
				return time.Second
			}
			return time.Duration(secs * float64(time.Second))
		}
	}
	secs := float64(leaseSeconds) / 3
	if secs < 1 {
		return time.Second
	}
	return time.Duration(secs * float64(time.Second))
}

// Validate mirrors BladeServiceDiscoveryOptions.Validate: reject a
// misconfigured registration before dialing.
func Validate(opts Options) error {
	if strings.TrimSpace(opts.Service) == "" {
		return errConfig("service name is required")
	}
	if strings.TrimSpace(opts.InstanceID) == "" {
		return errConfig("instance ID is required")
	}
	if strings.TrimSpace(opts.HttpEndpoint) == "" && strings.TrimSpace(opts.GrpcEndpoint) == "" {
		return errConfig("at least one endpoint is required")
	}
	if opts.LeaseSeconds < 3 {
		return errConfig("lease must be at least three seconds")
	}
	if opts.Weight < 1 {
		return errConfig("weight must be greater than zero")
	}
	return nil
}

func errConfig(msg string) error {
	return fmt.Errorf("service discovery: %w: %s", errors.New("invalid configuration"), msg)
}
