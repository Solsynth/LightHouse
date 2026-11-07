// Package grpcclient holds the outbound gRPC clients a Lighthouse service uses
// to call its siblings. Targets come from the [services] config section; an
// empty target leaves the corresponding client nil unless Blade discovery is
// enabled, in which case the service name is resolved from the live registry.
// This matches the C# services' behaviour with a dependency down.
package grpcclient

import (
	"crypto/tls"
	"fmt"
	"net/url"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"src.solsynth.dev/sosys/lighthouse/shared/platform/discovery"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// Dial creates a client connection for a fleet target. A scheme decides the
// transport: http/grpc is plaintext (Blade's h2c gateway), https/grpcs is TLS
// with CA validation skipped because fleet certificates are self-signed. A bare
// host:port defaults to TLS, the fleet convention.
func Dial(target string) (*grpc.ClientConn, error) {
	address, useTLS := normalize(target, true)
	if address == "" {
		return nil, fmt.Errorf("empty gRPC target")
	}
	var creds credentials.TransportCredentials
	if useTLS {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true})
	} else {
		creds = insecure.NewCredentials()
	}
	return grpc.NewClient(address, grpc.WithTransportCredentials(creds))
}

// normalize splits a target into address and transport.
func normalize(raw string, defaultTLS bool) (string, bool) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return "", defaultTLS
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme == "" {
		return target, defaultTLS
	}
	switch strings.ToLower(parsed.Scheme) {
	case "grpc", "http":
		return parsed.Host, false
	case "grpcs", "https":
		return parsed.Host, true
	default:
		return target, defaultTLS
	}
}

// Clients holds the outbound clients. A nil client means the integration is
// disabled or unconfigured; callers must gate the dependent feature.
type Clients struct {
	// Stargate hosts auth, profile, permission, action log and MLS.
	Account     gen.DyAccountServiceClient
	Profile     gen.DyProfileServiceClient
	Permission  gen.DyPermissionServiceClient
	ActionLog   gen.DyActionLogServiceClient
	Realm       gen.DyRealmServiceClient
	Mls         gen.DyMlsServiceClient
	Presence    gen.DyPresenceServiceClient
	DomainBlock gen.DyDomainBlockServiceClient

	Ring   gen.DyRingServiceClient
	Drive  gen.DyFileServiceClient
	Wallet gen.DyPaymentServiceClient
	Subs   gen.DySubscriptionServiceClient
	Merch  gen.DyMerchantServiceClient
	Dev    gen.DyCustomAppServiceClient
	Blade  gen.WebSocketServiceClient

	// Sphere is only configured in the Messager service.
	Posts        gen.DyPostServiceClient
	Publishers   gen.DyPublisherServiceClient
	Surveys      gen.DySurveyServiceClient
	Stickers     gen.DyStickerServiceClient
	Autocomplete gen.DyAutocompletionServiceClient
	WebReader    gen.DyWebReaderServiceClient
	Embeddings   gen.DyEmbeddingServiceClient

	conns []*grpc.ClientConn
}

// Targets is the [services] configuration a caller passes in.
type Targets struct {
	Stargate string
	Sphere   string
	Messager string
	Ring     string
	Drive    string
	Wallet   string
	Develop  string
	Blade    string
}

// New creates clients only for configured targets. Empty targets leave the
// corresponding client nil, which disables that integration. Use
// NewWithResolver when Blade discovery is enabled.
func New(targets Targets) (*Clients, error) {
	return NewWithResolver(targets, nil)
}

// NewWithResolver creates clients for every fleet service. With a resolver,
// each connection targets blade:///<service> and follows the live registry;
// the static target becomes the fallback while a service has no registration.
// A resolver therefore makes an empty [services] target usable: the service is
// discovered at connect time instead of being disabled.
func NewWithResolver(targets Targets, resolver *discovery.Resolver) (*Clients, error) {
	clients := &Clients{}
	if resolver != nil {
		discovery.RegisterResolver(resolver)
	}
	dial := func(service, target string, defaultTLS bool) (*grpc.ClientConn, error) {
		address, useTLS := normalize(target, defaultTLS)
		if resolver != nil {
			address = discovery.Target(service)
		}
		if address == "" {
			return nil, nil
		}
		if discovery.IsTarget(address) && resolver == nil {
			return nil, fmt.Errorf("dial %s: service discovery is not configured", service)
		}
		var creds credentials.TransportCredentials
		if useTLS {
			creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true})
		} else {
			creds = insecure.NewCredentials()
		}
		conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(creds))
		if err != nil {
			return nil, fmt.Errorf("dial %s: %w", target, err)
		}
		clients.conns = append(clients.conns, conn)
		return conn, nil
	}

	if conn, err := dial("stargate", targets.Stargate, true); err != nil {
		return nil, err
	} else if conn != nil {
		clients.Account = gen.NewDyAccountServiceClient(conn)
		clients.Permission = gen.NewDyPermissionServiceClient(conn)
		clients.ActionLog = gen.NewDyActionLogServiceClient(conn)
		clients.Mls = gen.NewDyMlsServiceClient(conn)
		clients.Presence = gen.NewDyPresenceServiceClient(conn)
		// Stargate also owns the profile, realm and domain-block services the
		// fleet used to dial Passport for.
		clients.Profile = gen.NewDyProfileServiceClient(conn)
		clients.Realm = gen.NewDyRealmServiceClient(conn)
		clients.DomainBlock = gen.NewDyDomainBlockServiceClient(conn)
	}
	if conn, err := dial("ring", targets.Ring, true); err != nil {
		return nil, err
	} else if conn != nil {
		clients.Ring = gen.NewDyRingServiceClient(conn)
	}
	if conn, err := dial("drive", targets.Drive, true); err != nil {
		return nil, err
	} else if conn != nil {
		clients.Drive = gen.NewDyFileServiceClient(conn)
	}
	if conn, err := dial("wallet", targets.Wallet, true); err != nil {
		return nil, err
	} else if conn != nil {
		clients.Wallet = gen.NewDyPaymentServiceClient(conn)
		clients.Subs = gen.NewDySubscriptionServiceClient(conn)
		clients.Merch = gen.NewDyMerchantServiceClient(conn)
	}
	if conn, err := dial("develop", targets.Develop, true); err != nil {
		return nil, err
	} else if conn != nil {
		clients.Dev = gen.NewDyCustomAppServiceClient(conn)
	}
	// Blade serves TLS like the rest of the fleet; a bare target keeps TLS and
	// an explicit http:// scheme selects the plaintext h2c path for older
	// deployments. Discovery keeps that transport choice while the address
	// follows the registry.
	if conn, err := dial("blade", targets.Blade, true); err != nil {
		return nil, err
	} else if conn != nil {
		clients.Blade = gen.NewWebSocketServiceClient(conn)
	}
	if conn, err := dial("sphere", targets.Sphere, true); err != nil {
		return nil, err
	} else if conn != nil {
		clients.Posts = gen.NewDyPostServiceClient(conn)
		clients.Publishers = gen.NewDyPublisherServiceClient(conn)
		clients.Surveys = gen.NewDySurveyServiceClient(conn)
		clients.Stickers = gen.NewDyStickerServiceClient(conn)
		clients.Autocomplete = gen.NewDyAutocompletionServiceClient(conn)
		clients.WebReader = gen.NewDyWebReaderServiceClient(conn)
		clients.Embeddings = gen.NewDyEmbeddingServiceClient(conn)
	}
	return clients, nil
}

// Close closes every dialed connection.
func (c *Clients) Close() {
	if c == nil {
		return
	}
	for _, conn := range c.conns {
		_ = conn.Close()
	}
	c.conns = nil
}
