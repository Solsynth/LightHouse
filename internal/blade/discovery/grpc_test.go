package discovery

import (
	"context"
	"testing"
	"time"

	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

func TestGRPCServiceRegistersWithoutCredential(t *testing.T) {
	registry := NewRegistry(nil, "test:discovery", time.Minute)
	service := NewGRPCService(registry)

	response, err := service.Register(context.Background(), &gen.DyRegisterServiceInstanceRequest{
		Instance: &gen.DyServiceInstance{
			Service: "sphere", InstanceId: "sphere-1", HttpEndpoint: "http://sphere:8000",
		},
		LeaseSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if response.GetInstance().GetService() != "sphere" {
		t.Fatalf("registered service = %q", response.GetInstance().GetService())
	}
}
