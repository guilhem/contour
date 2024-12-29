// Copyright Project Contour Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dag

import (
	"reflect"
	"testing"
	"time"

	v1 "github.com/projectcontour/contour/apis/projectcontour/v1"
	contour_v1alpha1 "github.com/projectcontour/contour/apis/projectcontour/v1alpha1"
	"github.com/projectcontour/contour/internal/timeout"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networking_v1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestHttpPaths(t *testing.T) {
	tests := map[string]struct {
		rule networking_v1.IngressRule
		want []networking_v1.HTTPIngressPath
	}{
		"zero value": {
			rule: networking_v1.IngressRule{},
			want: nil,
		},
		"empty paths": {
			rule: networking_v1.IngressRule{
				IngressRuleValue: networking_v1.IngressRuleValue{
					HTTP: &networking_v1.HTTPIngressRuleValue{},
				},
			},
			want: nil,
		},
		"several paths": {
			rule: networking_v1.IngressRule{
				IngressRuleValue: networking_v1.IngressRuleValue{
					HTTP: &networking_v1.HTTPIngressRuleValue{
						Paths: []networking_v1.HTTPIngressPath{{
							Backend: *backendv1("kuard", intstr.FromString("http")),
						}, {
							Path:    "/kuarder",
							Backend: *backendv1("kuarder", intstr.FromInt(8080)),
						}},
					},
				},
			},
			want: []networking_v1.HTTPIngressPath{{
				Backend: *backendv1("kuard", intstr.FromString("http")),
			}, {
				Path:    "/kuarder",
				Backend: *backendv1("kuarder", intstr.FromInt(8080)),
			}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := httppaths(tc.rule)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestIngressProcessor_ingressTimeoutPolicy(t *testing.T) {
	type fields struct {
		ResponseTimeout timeout.Setting
		ConnectTimeout  time.Duration
	}
	type args struct {
		ingress *networking_v1.Ingress
		log     logrus.FieldLogger
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   RouteTimeoutPolicy
	}{
		{
			name: "no timeout policy",
			fields: fields{
				ResponseTimeout: timeout.DefaultSetting(),
				ConnectTimeout:  0,
			},
			args: args{
				ingress: &networking_v1.Ingress{},
				log:     logrus.NewEntry(logrus.StandardLogger()),
			},
			want: RouteTimeoutPolicy{
				ResponseTimeout: timeout.DefaultSetting(),
			},
		},
		{
			name: "timeout policy",
			fields: fields{
				ResponseTimeout: timeout.DefaultSetting(),
				ConnectTimeout:  0,
			},
			args: args{
				ingress: &networking_v1.Ingress{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"projectcontour.io/response-timeout": "5s",
							"projectcontour.io/request-timeout":  "10s",
						},
					},
				},
				log: logrus.NewEntry(logrus.StandardLogger()),
			},
			want: RouteTimeoutPolicy{
				ResponseTimeout: timeout.DurationSetting(5 * time.Second),
			},
		},
		// {
		// 	name: "timeout policy with connect timeout",
		// 	fields: fields{
		// 		ResponseTimeout: timeout.DefaultSetting(),
		// 		ConnectTimeout:  0,
		// 	},
		// 	args: args{
		// 		ingress: &networking_v1.Ingress{
		// 			ObjectMeta: metav1.ObjectMeta{
		// 				Annotations: map[string]string{
		// 					"projectcontour.io/response-timeout": "5s",
		// 					"projectcontour.io/request-timeout":  "10s",
		// 					"projectcontour.io/connect-timeout":  "15s",
		// 				},
		// 			},
		// 		},
		// 		log: logrus.NewEntry(logrus.StandardLogger()),
		// 	},
		// 	want: RouteTimeoutPolicy{
		// 		ResponseTimeout: timeout.DurationSetting(5 * time.Second),
		// 		IdleTimeout:     timeout.DurationSetting(15 * time.Second),
		// 	},
		// },
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &IngressProcessor{
				ResponseTimeout: tt.fields.ResponseTimeout,
				ConnectTimeout:  tt.fields.ConnectTimeout,
			}
			if got := p.ingressTimeoutPolicy(tt.args.ingress, tt.args.log); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("IngressProcessor.ingressTimeoutPolicy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIngressProcessor_route_ratelimiting(t *testing.T) {
	type fields struct {
		FieldLogger                   logrus.FieldLogger
		dag                           *DAG
		source                        *KubernetesCache
		ClientCertificate             *types.NamespacedName
		EnableExternalNameService     bool
		RequestHeadersPolicy          *HeadersPolicy
		ResponseHeadersPolicy         *HeadersPolicy
		ResponseTimeout               timeout.Setting
		ConnectTimeout                time.Duration
		MaxRequestsPerConnection      *uint32
		PerConnectionBufferLimitBytes *uint32
		SetSourceMetadataOnRoutes     bool
		GlobalCircuitBreakerDefaults  *contour_v1alpha1.CircuitBreakers
		GlobalRateLimitService        *contour_v1alpha1.RateLimitServiceConfig
		UpstreamTLS                   *UpstreamTLS
	}
	type args struct {
		ingress          *networking_v1.Ingress
		host             string
		path             string
		pathType         networking_v1.PathType
		service          *Service
		clientCertSecret *Secret
		serviceName      string
		servicePort      int32
		log              logrus.FieldLogger
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *Route
		wantErr bool
	}{
		{
			name: "test GlobalRateLimitService",
			fields: fields{
				GlobalRateLimitService: &contour_v1alpha1.RateLimitServiceConfig{
					// ExtensionService: &contour_v1alpha1.RateLimitServiceExtensionRef{
					// 	Name: "test",
					// },
					// Domain: "test",
					DefaultGlobalRateLimitPolicy: &v1.GlobalRateLimitPolicy{
						Descriptors: []v1.RateLimitDescriptor{
							{
								Entries: []v1.RateLimitDescriptorEntry{
									{
										RemoteAddress: &v1.RemoteAddressDescriptor{},
									},
								},
							},
						},
					},
				},
			},

			args: args{
				ingress: &networking_v1.Ingress{},
				host:    "test",
				service: &Service{
					Protocol: "http",
				},
				log: logrus.New().WithField("test", "test GlobalRateLimitService"),
			},
			want: &Route{
				RateLimitPolicy: &RateLimitPolicy{
					Global: &GlobalRateLimitPolicy{
						Descriptors: []*RateLimitDescriptor{
							{
								Entries: []RateLimitDescriptorEntry{
									{
										RemoteAddress: &RemoteAddressDescriptorEntry{},
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "test GlobalRateLimitService with empty GlobalRateLimitPolicy",
			fields: fields{
				GlobalRateLimitService: &contour_v1alpha1.RateLimitServiceConfig{},
			},
			args: args{
				ingress: &networking_v1.Ingress{},
				host:    "test",
				service: &Service{
					Protocol: "http",
				},
				log: logrus.New().WithField("test", "test GlobalRateLimitService with empty GlobalRateLimitPolicy"),
			},
			want: &Route{
				RateLimitPolicy: nil,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &IngressProcessor{
				FieldLogger:                   tt.fields.FieldLogger,
				dag:                           tt.fields.dag,
				source:                        tt.fields.source,
				ClientCertificate:             tt.fields.ClientCertificate,
				EnableExternalNameService:     tt.fields.EnableExternalNameService,
				RequestHeadersPolicy:          tt.fields.RequestHeadersPolicy,
				ResponseHeadersPolicy:         tt.fields.ResponseHeadersPolicy,
				ResponseTimeout:               tt.fields.ResponseTimeout,
				ConnectTimeout:                tt.fields.ConnectTimeout,
				MaxRequestsPerConnection:      tt.fields.MaxRequestsPerConnection,
				PerConnectionBufferLimitBytes: tt.fields.PerConnectionBufferLimitBytes,
				SetSourceMetadataOnRoutes:     tt.fields.SetSourceMetadataOnRoutes,
				GlobalCircuitBreakerDefaults:  tt.fields.GlobalCircuitBreakerDefaults,
				GlobalRateLimitService:        tt.fields.GlobalRateLimitService,
				UpstreamTLS:                   tt.fields.UpstreamTLS,
			}
			got, err := p.route(tt.args.ingress, tt.args.host, tt.args.path, tt.args.pathType, tt.args.service, tt.args.clientCertSecret, tt.args.serviceName, tt.args.servicePort, tt.args.log)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want.RateLimitPolicy, got.RateLimitPolicy)
		})
	}
}
