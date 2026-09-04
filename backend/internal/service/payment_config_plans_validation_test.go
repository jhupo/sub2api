//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func validCreatePlanRequest() CreatePlanRequest {
	return CreatePlanRequest{Name: "Pro", Price: 9.99, ValidityDays: 30, ValidityUnit: "days"}
}

func TestValidatePlanRequired_AllValid(t *testing.T) {
	err := validatePlanRequired(validCreatePlanRequest())
	require.NoError(t, err)
}

func TestValidatePlanRequired_EmptyName(t *testing.T) {
	req := validCreatePlanRequest()
	req.Name = ""
	err := validatePlanRequired(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "plan name")
}

func TestValidatePlanRequired_WhitespaceName(t *testing.T) {
	req := validCreatePlanRequest()
	req.Name = "   "
	err := validatePlanRequired(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "plan name")
}

func TestValidatePlanRequired_ZeroPrice(t *testing.T) {
	req := validCreatePlanRequest()
	req.Price = 0
	err := validatePlanRequired(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "price")
}

func TestValidatePlanRequired_NegativePrice(t *testing.T) {
	req := validCreatePlanRequest()
	req.Price = -5
	err := validatePlanRequired(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "price")
}

func TestValidatePlanRequired_ZeroValidityDays(t *testing.T) {
	req := validCreatePlanRequest()
	req.ValidityDays = 0
	err := validatePlanRequired(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "validity days")
}

func TestValidatePlanRequired_NegativeValidityDays(t *testing.T) {
	req := validCreatePlanRequest()
	req.ValidityDays = -7
	err := validatePlanRequired(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "validity days")
}

func TestValidatePlanRequired_EmptyValidityUnit(t *testing.T) {
	req := validCreatePlanRequest()
	req.ValidityUnit = ""
	err := validatePlanRequired(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "validity unit")
}

func TestValidatePlanRequired_WhitespaceValidityUnit(t *testing.T) {
	req := validCreatePlanRequest()
	req.ValidityUnit = "   "
	err := validatePlanRequired(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "validity unit")
}

func TestValidatePlanRequired_NameValidatedFirst(t *testing.T) {
	err := validatePlanRequired(CreatePlanRequest{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "plan name")
}

func TestValidatePlanRequired_TrimmedValidName(t *testing.T) {
	req := validCreatePlanRequest()
	req.Name = "  Pro  "
	err := validatePlanRequired(req)
	require.NoError(t, err)
}

func TestValidatePlanRequired_NegativeOriginalPrice(t *testing.T) {
	neg := -10.0
	req := validCreatePlanRequest()
	req.OriginalPrice = &neg
	err := validatePlanRequired(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "original price")
}

func TestValidatePlanRequired_ZeroOriginalPrice(t *testing.T) {
	zero := 0.0
	req := validCreatePlanRequest()
	req.OriginalPrice = &zero
	err := validatePlanRequired(req)
	require.NoError(t, err)
}

func TestValidatePlanRequired_ValidOriginalPrice(t *testing.T) {
	op := 19.99
	req := validCreatePlanRequest()
	req.OriginalPrice = &op
	err := validatePlanRequired(req)
	require.NoError(t, err)
}

// --- validatePlanPatch tests ---

func TestValidatePlanPatch_NegativeOriginalPrice(t *testing.T) {
	neg := -5.0
	err := validatePlanPatch(UpdatePlanRequest{OriginalPrice: OptionalNullableFloat{Set: true, Value: &neg}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "original price")
}

func TestValidatePlanPatch_ZeroOriginalPrice(t *testing.T) {
	zero := 0.0
	err := validatePlanPatch(UpdatePlanRequest{OriginalPrice: OptionalNullableFloat{Set: true, Value: &zero}})
	require.NoError(t, err)
}

func TestValidatePlanPatch_ValidOriginalPrice(t *testing.T) {
	op := 29.99
	err := validatePlanPatch(UpdatePlanRequest{OriginalPrice: OptionalNullableFloat{Set: true, Value: &op}})
	require.NoError(t, err)
}

func TestValidatePlanPatch_NilOriginalPrice(t *testing.T) {
	err := validatePlanPatch(UpdatePlanRequest{})
	require.NoError(t, err)
}

// --- validatePlanPatch: other fields ---

func ptrStr(s string) *string     { return &s }
func ptrInt(i int) *int           { return &i }
func ptrInt64(i int64) *int64     { return &i }
func ptrFloat(f float64) *float64 { return &f }

func TestValidatePlanPatch_EmptyName(t *testing.T) {
	err := validatePlanPatch(UpdatePlanRequest{Name: ptrStr("")})
	require.Error(t, err)
	require.Contains(t, err.Error(), "plan name")
}

func TestValidatePlanPatch_ValidName(t *testing.T) {
	err := validatePlanPatch(UpdatePlanRequest{Name: ptrStr("Basic")})
	require.NoError(t, err)
}

func TestValidatePlanPatch_NegativePrice(t *testing.T) {
	err := validatePlanPatch(UpdatePlanRequest{Price: ptrFloat(-1)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "price")
}

func TestValidatePlanPatch_ZeroPrice(t *testing.T) {
	err := validatePlanPatch(UpdatePlanRequest{Price: ptrFloat(0)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "price")
}

func TestValidatePlanPatch_ValidPrice(t *testing.T) {
	err := validatePlanPatch(UpdatePlanRequest{Price: ptrFloat(9.99)})
	require.NoError(t, err)
}

func TestValidatePlanPatch_ZeroValidityDays(t *testing.T) {
	err := validatePlanPatch(UpdatePlanRequest{ValidityDays: ptrInt(0)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "validity days")
}

func TestValidatePlanPatch_EmptyValidityUnit(t *testing.T) {
	err := validatePlanPatch(UpdatePlanRequest{ValidityUnit: ptrStr("")})
	require.Error(t, err)
	require.Contains(t, err.Error(), "validity unit")
}

func TestValidatePlanPatch_ValidValidityUnit(t *testing.T) {
	err := validatePlanPatch(UpdatePlanRequest{ValidityUnit: ptrStr("days")})
	require.NoError(t, err)
}

func TestValidatePlanPatch_AllNil(t *testing.T) {
	err := validatePlanPatch(UpdatePlanRequest{})
	require.NoError(t, err)
}

// --- normalizePlanCurrency tests ---
// Empty must stay empty (not coerced to the default payment currency),
// so existing plans keep rendering without any currency label.

func TestNormalizePlanCurrency_EmptyKeepsEmpty(t *testing.T) {
	currency, err := normalizePlanCurrency("")
	require.NoError(t, err)
	require.Equal(t, "", currency)
}

func TestNormalizePlanCurrency_WhitespaceKeepsEmpty(t *testing.T) {
	currency, err := normalizePlanCurrency("   ")
	require.NoError(t, err)
	require.Equal(t, "", currency)
}

func TestNormalizePlanCurrency_LowercaseNormalized(t *testing.T) {
	currency, err := normalizePlanCurrency("nzd")
	require.NoError(t, err)
	require.Equal(t, "NZD", currency)
}

func TestNormalizePlanCurrency_ValidUppercase(t *testing.T) {
	currency, err := normalizePlanCurrency("USD")
	require.NoError(t, err)
	require.Equal(t, "USD", currency)
}

func TestNormalizePlanCurrency_TooShort(t *testing.T) {
	_, err := normalizePlanCurrency("NZ")
	require.Error(t, err)
	require.Contains(t, err.Error(), "currency")
}

func TestNormalizePlanCurrency_TooLong(t *testing.T) {
	_, err := normalizePlanCurrency("NZDD")
	require.Error(t, err)
	require.Contains(t, err.Error(), "currency")
}

func TestNormalizePlanCurrency_NonLetter(t *testing.T) {
	_, err := normalizePlanCurrency("N2D")
	require.Error(t, err)
	require.Contains(t, err.Error(), "currency")
}
