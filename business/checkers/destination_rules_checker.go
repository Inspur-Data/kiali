package checkers

import (
	networking_v1alpha3 "istio.io/client-go/pkg/apis/networking/v1alpha3"

	"github.com/kiali/kiali/business/checkers/common"
	"github.com/kiali/kiali/business/checkers/destinationrules"
	"github.com/kiali/kiali/kubernetes"
	"github.com/kiali/kiali/models"
)

const DestinationRuleCheckerType = "destinationrule"

type DestinationRulesChecker struct {
	DestinationRules         []networking_v1alpha3.DestinationRule
	ExportedDestinationRules []networking_v1alpha3.DestinationRule
	MTLSDetails              kubernetes.MTLSDetails
	ExportedServiceEntries   []networking_v1alpha3.ServiceEntry
	Namespaces               []models.Namespace
}

func (in DestinationRulesChecker) Check() models.IstioValidations {
	validations := models.IstioValidations{}

	validations = validations.MergeValidations(in.runIndividualChecks())
	validations = validations.MergeValidations(in.runGroupChecks())

	return validations
}

func (in DestinationRulesChecker) runGroupChecks() models.IstioValidations {
	validations := models.IstioValidations{}

	seHosts := kubernetes.ServiceEntryHostnames(in.ExportedServiceEntries)

	enabledDRCheckers := []GroupChecker{
		destinationrules.MultiMatchChecker{Namespaces: in.Namespaces, ServiceEntries: seHosts, ExportedDestinationRules: in.ExportedDestinationRules},
	}

	// Appending validations that only applies to non-autoMTLS meshes
	if !in.MTLSDetails.EnabledAutoMtls {
		enabledDRCheckers = append(enabledDRCheckers, destinationrules.TrafficPolicyChecker{DestinationRules: in.DestinationRules, ExportedDestinationRules: in.ExportedDestinationRules, MTLSDetails: in.MTLSDetails})
	}

	for _, checker := range enabledDRCheckers {
		validations = validations.MergeValidations(checker.Check())
	}

	return validations
}

func (in DestinationRulesChecker) runIndividualChecks() models.IstioValidations {
	validations := models.IstioValidations{}

	for _, destinationRule := range in.DestinationRules {
		validations.MergeValidations(in.runChecks(destinationRule))
	}

	return validations
}

func (in DestinationRulesChecker) runChecks(destinationRule networking_v1alpha3.DestinationRule) models.IstioValidations {
	destinationRuleName := destinationRule.Name
	key, rrValidation := EmptyValidValidation(destinationRuleName, destinationRule.Namespace, DestinationRuleCheckerType)

	enabledCheckers := []Checker{
		destinationrules.DisabledNamespaceWideMTLSChecker{DestinationRule: destinationRule, MTLSDetails: in.MTLSDetails},
		destinationrules.DisabledMeshWideMTLSChecker{DestinationRule: destinationRule, MeshPeerAuthns: in.MTLSDetails.MeshPeerAuthentications},
		common.ExportToNamespaceChecker{ExportTo: destinationRule.Spec.ExportTo, Namespaces: in.Namespaces},
	}

	// Appending validations that only applies to non-autoMTLS meshes
	if !in.MTLSDetails.EnabledAutoMtls {
		enabledCheckers = append(enabledCheckers, destinationrules.NamespaceWideMTLSChecker{DestinationRule: destinationRule, MTLSDetails: in.MTLSDetails})
		enabledCheckers = append(enabledCheckers, destinationrules.MeshWideMTLSChecker{DestinationRule: destinationRule, MTLSDetails: in.MTLSDetails})
	}

	for _, checker := range enabledCheckers {
		checks, validChecker := checker.Check()
		rrValidation.Checks = append(rrValidation.Checks, checks...)
		rrValidation.Valid = rrValidation.Valid && validChecker
	}

	return models.IstioValidations{key: rrValidation}
}
