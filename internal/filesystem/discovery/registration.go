// Package discovery registers DysonFS with Blade's service registry and
// resolves outbound services through the same registry. DysonFS builds outside
// the Go workspace, so it carries this small client instead of importing the
// shared platform's new packages.
package discovery

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// Options describes one DysonFS instance registration.
type Options struct {
	Service      string
	InstanceID   string
	HTTPEndpoint string
	GRPCEndpoint string
	LeaseSeconds int
	Weight       int
}

// Registration owns the register -> renew -> deregister lifecycle.
type Registration struct {
	client     gen.DyServiceDiscoveryServiceClient
	opts       Options
	log        zerolog.Logger
	registered bool
}

func NewRegistration(client gen.DyServiceDiscoveryServiceClient, opts Options, log zerolog.Logger) *Registration {
	return &Registration{client: client, opts: opts, log: log}
}

// Run registers the instance and keeps the lease renewed until ctx is
// cancelled, then deregisters on shutdown.
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
		} else if ctx.Err() != nil {
			return
		} else {
			r.registered = false
			r.log.Warn().
				Str("service", r.opts.Service).
				Str("instance_id", r.opts.InstanceID).
				Str("retry_in", retryDelay.String()).
				Err(err).
				Msg("blade service discovery registration failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryDelay):
		}
		retryDelay = time.Duration(math.Min(float64(retryDelay)*2, 30_000_000_000))
	}
}

func (r *Registration) register(ctx context.Context) (time.Duration, error) {
	instance := &gen.DyServiceInstance{
		Service:      r.opts.Service,
		InstanceId:   r.opts.InstanceID,
		HttpEndpoint: r.opts.HTTPEndpoint,
		GrpcEndpoint: r.opts.GRPCEndpoint,
		Weight:       int32(r.opts.Weight),
	}
	response, err := r.client.Register(ctx, &gen.DyRegisterServiceInstanceRequest{
		Instance:     instance,
		LeaseSeconds: int32(r.opts.LeaseSeconds),
	})
	if err != nil {
		return 0, err
	}
	r.log.Info().
		Str("service", r.opts.Service).
		Str("instance_id", r.opts.InstanceID).
		Msg("registered with blade service discovery")
	return renewalInterval(response.GetLeaseExpiresAtUnixMs(), r.opts.LeaseSeconds), nil
}

func (r *Registration) renewLoop(ctx context.Context, interval time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		response, err := r.client.Renew(ctx, &gen.DyRenewServiceLeaseRequest{
			Service:      r.opts.Service,
			InstanceId:   r.opts.InstanceID,
			LeaseSeconds: int32(r.opts.LeaseSeconds),
		})
		if err != nil {
			if status.Code(err) == codes.NotFound {
				r.log.Info().
					Str("service", r.opts.Service).
					Str("instance_id", r.opts.InstanceID).
					Msg("blade discovery lease not found; re-registering")
			} else {
				r.log.Warn().
					Str("service", r.opts.Service).
					Str("instance_id", r.opts.InstanceID).
					Err(err).
					Msg("blade discovery lease renewal failed")
			}
			return
		}
		interval = renewalInterval(response.GetLeaseExpiresAtUnixMs(), r.opts.LeaseSeconds)
	}
}

// Deregister removes the instance from the registry (best-effort).
func (r *Registration) Deregister(ctx context.Context) {
	if !r.registered {
		return
	}
	_, err := r.client.Deregister(ctx, &gen.DyDeregisterServiceInstanceRequest{
		Service:    r.opts.Service,
		InstanceId: r.opts.InstanceID,
	})
	if err != nil {
		r.log.Warn().
			Str("service", r.opts.Service).
			Str("instance_id", r.opts.InstanceID).
			Err(err).
			Msg("blade service discovery deregistration failed")
		return
	}
	r.registered = false
}

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

// Validate rejects a misconfigured registration before dialing.
func Validate(opts Options) error {
	if strings.TrimSpace(opts.Service) == "" {
		return errors.New("service discovery: service name is required")
	}
	if strings.TrimSpace(opts.InstanceID) == "" {
		return errors.New("service discovery: instance ID is required")
	}
	if strings.TrimSpace(opts.HTTPEndpoint) == "" && strings.TrimSpace(opts.GRPCEndpoint) == "" {
		return errors.New("service discovery: at least one endpoint is required")
	}
	if opts.LeaseSeconds < 3 {
		return errors.New("service discovery: lease must be at least three seconds")
	}
	if opts.Weight < 1 {
		return errors.New("service discovery: weight must be greater than zero")
	}
	return nil
}

func normalizeService(service string) string {
	return strings.ToLower(strings.TrimSpace(service))
}

func endpoint(instance *gen.DyServiceInstance, protocol string) string {
	if instance == nil {
		return ""
	}
	for name, value := range instance.GetEndpoints() {
		if strings.EqualFold(strings.TrimSpace(name), protocol) {
			return strings.TrimSpace(value)
		}
	}
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "http":
		return strings.TrimSpace(instance.GetHttpEndpoint())
	case "grpc":
		return strings.TrimSpace(instance.GetGrpcEndpoint())
	default:
		return ""
	}
}
