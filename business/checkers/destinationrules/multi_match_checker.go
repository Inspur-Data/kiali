package destinationrules

import (
	"fmt"
	"strings"

	networking_v1alpha3 "istio.io/client-go/pkg/apis/networking/v1alpha3"

	"github.com/kiali/kiali/kubernetes"
	"github.com/kiali/kiali/models"
)

const DestinationRulesCheckerType = "destinationrule"

type MultiMatchChecker struct {
	ExportedDestinationRules []networking_v1alpha3.DestinationRule
	ServiceEntries           map[string][]string
	Namespaces               models.Namespaces
}

type subset struct {
	Name      string
	Namespace string
	RuleName  string
}

type rule struct {
	Name      string
	Namespace string
}

// Check validates that no two destinationRules target the same host+subset combination
func (m MultiMatchChecker) Check() models.IstioValidations {
	validations := models.IstioValidations{}

	// Equality search is: [fqdn.String()][subset] except for ServiceEntry targets which use [host][subset]
	seenHostSubsets := make(map[string]map[string][]rule)

	for _, dr := range m.ExportedDestinationRules {
		destinationRulesName := dr.Name
		destinationRulesNamespace := dr.Namespace
		fqdn := kubernetes.GetHost(dr.Spec.Host, dr.Namespace, dr.ClusterName, m.Namespaces.GetNames())

		// Skip DR validation if it enables mTLS either namespace or mesh-wide
		if isNonLocalmTLSForServiceEnabled(dr, fqdn.String()) {
			continue
		}

		foundSubsets := extractSubsets(dr, destinationRulesName, destinationRulesNamespace)

		if fqdn.IsWildcard() {
			// We need to check the matching subsets from all hosts now
			for _, h := range seenHostSubsets {
				checkCollisions(validations, destinationRulesNamespace, destinationRulesName, foundSubsets, h)
			}
			// We add * later
		}
		// Search "*" first and then exact name
		if previous, found := seenHostSubsets[fmt.Sprintf("*.%s.%s", fqdn.Namespace, fqdn.Cluster)]; found {
			// Need to check subsets of "*"
			checkCollisions(validations, destinationRulesNamespace, destinationRulesName, foundSubsets, previous)
		}

		if previous, found := seenHostSubsets[fqdn.String()]; found {
			// Host found, need to check underlying subsets
			checkCollisions(validations, destinationRulesNamespace, destinationRulesName, foundSubsets, previous)
		}
		// Nothing threw an error, so add these
		if _, found := seenHostSubsets[fqdn.String()]; !found {
			seenHostSubsets[fqdn.String()] = make(map[string][]rule)
		}
		for _, s := range foundSubsets {
			seenHostSubsets[fqdn.String()][s.Name] = append(seenHostSubsets[fqdn.String()][s.Name], rule{destinationRulesName, destinationRulesNamespace})
		}

	}

	return validations
}

func isNonLocalmTLSForServiceEnabled(dr networking_v1alpha3.DestinationRule, service string) bool {
	return strings.HasPrefix(service, "*") && ismTLSEnabled(dr)
}

func ismTLSEnabled(dr networking_v1alpha3.DestinationRule) bool {
	if dr.Spec.TrafficPolicy != nil && dr.Spec.TrafficPolicy.Tls != nil {
		mode := dr.Spec.TrafficPolicy.Tls.Mode.String()
		return mode == "ISTIO_MUTUAL"
	}
	return false
}

func extractSubsets(dr networking_v1alpha3.DestinationRule, destinationRulesName string, destinationRulesNamespace string) []subset {
	if len(dr.Spec.Subsets) > 0 {
		foundSubsets := []subset{}
		for _, ss := range dr.Spec.Subsets {
			foundSubsets = append(foundSubsets, subset{
				Name:      ss.Name,
				Namespace: destinationRulesNamespace,
				RuleName:  destinationRulesName,
			})
		}
		return foundSubsets
	}
	// Matches all the subsets:~
	return []subset{{"~", destinationRulesNamespace, destinationRulesName}}
}

func checkCollisions(validations models.IstioValidations, namespace, destinationRulesName string, foundSubsets []subset, existing map[string][]rule) {
	// If current subset is ~
	if len(foundSubsets) == 1 && foundSubsets[0].Name == "~" {
		// This should match any subset in the same hostname
		for _, v := range existing {
			for _, e := range v {
				addError(validations, []string{namespace, e.Namespace}, []string{destinationRulesName, e.Name})
			}
		}
	}

	// If we have existing subset with ~
	if rules, found := existing["~"]; found {
		for _, rule := range rules {
			addError(validations, []string{namespace, rule.Namespace}, []string{destinationRulesName, rule.Name})
		}
	}

	for _, s := range foundSubsets {
		if rules, found := existing[s.Name]; found {
			for _, rule := range rules {
				addError(validations, []string{namespace, rule.Namespace}, []string{destinationRulesName, rule.Name})
			}
		}
	}
}

// addError links new validation errors to the validations. namespaces nad destinationRuleNames must always be a pair
func addError(validations models.IstioValidations, namespaces []string, destinationRuleNames []string) models.IstioValidations {
	key0, rrValidation0 := createError("destinationrules.multimatch", namespaces[0], destinationRuleNames[0], true)
	key1, rrValidation1 := createError("destinationrules.multimatch", namespaces[1], destinationRuleNames[1], true)

	rrValidation0.References = append(rrValidation0.References, key1)
	rrValidation1.References = append(rrValidation1.References, key0)

	validations.MergeValidations(models.IstioValidations{key0: rrValidation0})
	validations.MergeValidations(models.IstioValidations{key1: rrValidation1})

	return validations
}

func createError(errorText, namespace, destinationRuleName string, valid bool) (models.IstioValidationKey, *models.IstioValidation) {
	key := models.IstioValidationKey{Name: destinationRuleName, Namespace: namespace, ObjectType: DestinationRulesCheckerType}
	checks := models.Build(errorText, "spec/host")
	rrValidation := &models.IstioValidation{
		Name:       destinationRuleName,
		ObjectType: DestinationRulesCheckerType,
		Valid:      valid,
		Checks: []*models.IstioCheck{
			&checks,
		},
		References: make([]models.IstioValidationKey, 0),
	}

	return key, rrValidation
}
