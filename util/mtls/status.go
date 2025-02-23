package mtls

import (
	networking_v1alpha3 "istio.io/client-go/pkg/apis/networking/v1alpha3"
	security_v1beta "istio.io/client-go/pkg/apis/security/v1beta1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/kiali/kiali/kubernetes"
	"github.com/kiali/kiali/models"
)

const (
	MTLSEnabled          = "MTLS_ENABLED"
	MTLSPartiallyEnabled = "MTLS_PARTIALLY_ENABLED"
	MTLSNotEnabled       = "MTLS_NOT_ENABLED"
	MTLSDisabled         = "MTLS_DISABLED"
)

type MtlsStatus struct {
	Namespace           string
	PeerAuthentications []security_v1beta.PeerAuthentication
	DestinationRules    []networking_v1alpha3.DestinationRule
	MatchingLabels      labels.Labels
	ServiceList         models.ServiceList
	AutoMtlsEnabled     bool
	AllowPermissive     bool
	RegistryServices    []*kubernetes.RegistryService
}

type TlsStatus struct {
	DestinationRuleStatus    string
	PeerAuthenticationStatus string
	OverallStatus            string
}

type NameNamespace struct {
	Name      string
	Namespace string
}

func (m MtlsStatus) hasPeerAuthnNamespacemTLSDefinition() string {
	for _, p := range m.PeerAuthentications {
		if _, mode := kubernetes.PeerAuthnHasMTLSEnabled(p); mode != "" {
			return mode
		}
	}

	return ""
}

func (m MtlsStatus) hasDesinationRuleEnablingNamespacemTLS() string {
	for _, dr := range m.DestinationRules {
		if _, mode := kubernetes.DestinationRuleHasNamespaceWideMTLSEnabled(m.Namespace, dr); mode != "" {
			return mode
		}
	}

	return ""
}

// Returns the mTLS status at workload level (matching the m.MatchingLabels)
func (m MtlsStatus) WorkloadMtlsStatus() string {
	for _, pa := range m.PeerAuthentications {
		var selectorLabels map[string]string
		if pa.Spec.Selector != nil {
			selectorLabels = pa.Spec.Selector.MatchLabels
		} else {
			continue
		}
		selector := labels.Set(selectorLabels).AsSelector()
		match := selector.Matches(m.MatchingLabels)
		if !match {
			continue
		}

		_, mode := kubernetes.PeerAuthnMTLSMode(pa)
		if mode == "STRICT" {
			return MTLSEnabled
		} else if mode == "DISABLE" {
			return MTLSDisabled
		} else if mode == "PERMISSIVE" {
			if len(m.DestinationRules) == 0 {
				return MTLSNotEnabled
			} else {
				// Filter DR that applies to the Services matching with the selector
				// Fetch hosts from DRs and its mtls mode [details, ISTIO_STATUS]
				// Filter Svc and extract its workloads selectors
				filteredSvcs := m.ServiceList.FilterServicesForSelector(selector)
				filteredRSvcs := kubernetes.FilterRegistryServicesBySelector(selector, m.Namespace, m.RegistryServices)
				nameNamespaces := []NameNamespace{}
				for _, svc := range filteredSvcs {
					nameNamespaces = append(nameNamespaces, NameNamespace{svc.Name, svc.Namespace})
				}
				for _, rSvc := range filteredRSvcs {
					nameNamespaces = append(nameNamespaces, NameNamespace{rSvc.IstioService.Attributes.Name, rSvc.IstioService.Attributes.Namespace})
				}
				for _, nameNamespace := range nameNamespaces {
					filteredDrs := kubernetes.FilterDestinationRulesByService(m.DestinationRules, nameNamespace.Namespace, nameNamespace.Name)
					for _, dr := range filteredDrs {
						enabled, mode := kubernetes.DestinationRuleHasMTLSEnabled(dr)
						if enabled || mode == "MUTUAL" {
							return MTLSEnabled
						} else if mode == "DISABLE" {
							return MTLSDisabled
						}
					}
				}

				return MTLSNotEnabled
			}
		}
	}

	return MTLSNotEnabled
}

func (m MtlsStatus) NamespaceMtlsStatus() TlsStatus {
	drStatus := m.hasDesinationRuleEnablingNamespacemTLS()
	paStatus := m.hasPeerAuthnNamespacemTLSDefinition()
	return m.finalStatus(drStatus, paStatus)
}

func (m MtlsStatus) finalStatus(drStatus, paStatus string) TlsStatus {
	finalStatus := MTLSPartiallyEnabled

	mtlsEnabled := drStatus == "ISTIO_MUTUAL" || drStatus == "MUTUAL" || (drStatus == "" && m.AutoMtlsEnabled)
	mtlsDisabled := drStatus == "DISABLE" || (drStatus == "" && m.AutoMtlsEnabled)

	if (paStatus == "STRICT" || (paStatus == "PERMISSIVE" && m.AllowPermissive)) && mtlsEnabled {
		finalStatus = MTLSEnabled
	} else if paStatus == "DISABLE" && mtlsDisabled {
		finalStatus = MTLSDisabled
	} else if paStatus == "" && drStatus == "" {
		finalStatus = MTLSNotEnabled
	}

	return TlsStatus{
		DestinationRuleStatus:    drStatus,
		PeerAuthenticationStatus: paStatus,
		OverallStatus:            finalStatus,
	}
}

func (m MtlsStatus) MeshMtlsStatus() TlsStatus {
	drStatus := m.hasDestinationRuleMeshTLSDefinition()
	paStatus := m.hasPeerAuthnMeshTLSDefinition()
	return TlsStatus{
		DestinationRuleStatus:    drStatus,
		PeerAuthenticationStatus: paStatus,
		OverallStatus:            m.OverallMtlsStatus(TlsStatus{}, m.finalStatus(drStatus, paStatus)),
	}
}

func (m MtlsStatus) hasPeerAuthnMeshTLSDefinition() string {
	for _, mp := range m.PeerAuthentications {
		if _, mode := kubernetes.PeerAuthnHasMTLSEnabled(mp); mode != "" {
			return mode
		}
	}
	return ""
}

func (m MtlsStatus) hasDestinationRuleMeshTLSDefinition() string {
	for _, dr := range m.DestinationRules {
		if _, mode := kubernetes.DestinationRuleHasMTLSEnabledForHost("*.local", dr); mode != "" {
			return mode
		}
	}
	return ""
}

func (m MtlsStatus) OverallMtlsStatus(nsStatus, meshStatus TlsStatus) string {
	var status = MTLSPartiallyEnabled
	if nsStatus.hasDefinedTls() {
		status = nsStatus.OverallStatus
	} else if nsStatus.hasPartialTlsConfig() {
		status = m.inheritedOverallStatus(nsStatus, meshStatus)
	} else if meshStatus.hasDefinedTls() {
		status = meshStatus.OverallStatus
	} else if meshStatus.hasNoConfig() {
		status = MTLSNotEnabled
	} else if meshStatus.hasPartialDisabledConfig() {
		status = MTLSDisabled
	} else if meshStatus.hasHalfTlsConfigDefined(m.AutoMtlsEnabled, m.AllowPermissive) {
		status = MTLSEnabled
	} else if !m.AutoMtlsEnabled && meshStatus.hasPartialTlsConfig() {
		status = MTLSPartiallyEnabled
	}
	return status
}

func (m MtlsStatus) inheritedOverallStatus(nsStatus, meshStatus TlsStatus) string {
	var partialDRStatus, partialPAStatus = nsStatus.DestinationRuleStatus, nsStatus.PeerAuthenticationStatus
	if nsStatus.DestinationRuleStatus == "" {
		partialDRStatus = meshStatus.DestinationRuleStatus
	}

	if nsStatus.PeerAuthenticationStatus == "" {
		partialPAStatus = meshStatus.PeerAuthenticationStatus
	}

	return m.OverallMtlsStatus(TlsStatus{},
		m.finalStatus(partialDRStatus, partialPAStatus),
	)
}

func (t TlsStatus) hasDefinedTls() bool {
	return t.OverallStatus == MTLSEnabled || t.OverallStatus == MTLSDisabled
}

func (t TlsStatus) hasPartialTlsConfig() bool {
	return t.OverallStatus == MTLSPartiallyEnabled
}

func (t TlsStatus) hasHalfTlsConfigDefined(autoMtls, allowPermissive bool) bool {
	defined := false
	if autoMtls {
		defined = t.PeerAuthenticationStatus == "STRICT" && t.DestinationRuleStatus == "" ||
			(t.DestinationRuleStatus == "ISTIO_MUTUAL" || t.DestinationRuleStatus == "MUTUAL") && t.PeerAuthenticationStatus == ""

		if !defined && allowPermissive {
			defined = t.PeerAuthenticationStatus == "PERMISSIVE" && t.DestinationRuleStatus == ""
		}
	}

	return defined
}

func (t TlsStatus) hasNoConfig() bool {
	return t.PeerAuthenticationStatus == "" && t.DestinationRuleStatus == ""
}

func (t TlsStatus) hasPartialDisabledConfig() bool {
	return t.PeerAuthenticationStatus == "DISABLE" && t.DestinationRuleStatus == "" ||
		t.DestinationRuleStatus == "DISABLE" && t.PeerAuthenticationStatus == ""
}
