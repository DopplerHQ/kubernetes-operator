package transformers

import "testing"

type testCase struct {
	name          string
	nameTransform string
}

func TestCamel(t *testing.T) {
	testCases := []testCase{
		{"TEST", "test"},
		{"TEST_", "test"},
		{"TEST_SECRET", "testSecret"},
		{"TEST_SECRET_NAME", "testSecretName"},
		{"TEST__SECRET", "testSecret"},
	}

	for _, testCase := range testCases {
		nameTransform := Camel(testCase.name)
		if testCase.nameTransform != nameTransform {
			t.Errorf("Expected '%s' to be '%s' but got '%s'", testCase.name, testCase.nameTransform, nameTransform)
		}
	}
}

func TestUpperCamel(t *testing.T) {
	testCases := []testCase{
		{"TEST", "Test"},
		{"TEST_", "Test"},
		{"TEST_SECRET", "TestSecret"},
		{"TEST_SECRET_NAME", "TestSecretName"},
		{"TEST__SECRET", "TestSecret"},
	}

	for _, testCase := range testCases {
		nameTransform := UpperCamel(testCase.name)
		if testCase.nameTransform != nameTransform {
			t.Errorf("Expected '%s' to be '%s' but got '%s'", testCase.name, testCase.nameTransform, nameTransform)
		}
	}
}

func TestLowerSnake(t *testing.T) {
	testCases := []testCase{
		{"TEST", "test"},
		{"TEST_", "test_"},
		{"TEST_SECRET", "test_secret"},
		{"TEST_SECRET_NAME", "test_secret_name"},
		{"TEST__SECRET", "test__secret"},
	}

	for _, testCase := range testCases {
		nameTransform := LowerSnake(testCase.name)
		if testCase.nameTransform != nameTransform {
			t.Errorf("Expected '%s' to be '%s' but got '%s'", testCase.name, testCase.nameTransform, nameTransform)
		}
	}
}

func TestTFVar(t *testing.T) {
	testCases := []testCase{
		{"TEST", "TF_VAR_test"},
		{"TEST_", "TF_VAR_test_"},
		{"TEST_SECRET", "TF_VAR_test_secret"},
		{"TEST_SECRET_NAME", "TF_VAR_test_secret_name"},
		{"TEST__SECRET", "TF_VAR_test__secret"},
	}

	for _, testCase := range testCases {
		nameTransform := TFVar(testCase.name)
		if testCase.nameTransform != nameTransform {
			t.Errorf("Expected '%s' to be '%s' but got '%s'", testCase.name, testCase.nameTransform, nameTransform)
		}
	}
}

func TestDotNETEnv(t *testing.T) {
	type testCase struct {
		name          string
		nameTransform string
	}

	testCases := []testCase{
		{"TEST", "Test"},
		{"TEST_", "Test"},
		{"TEST_SECRET", "TestSecret"},
		{"TEST_SECRET_NAME", "TestSecretName"},
		{"TEST__SECRET", "Test__Secret"},
	}

	for _, testCase := range testCases {
		nameTransform := DotNETEnv(testCase.name)
		if testCase.nameTransform != nameTransform {
			t.Errorf("Expected '%s' to be '%s' but got '%s'", testCase.name, testCase.nameTransform, nameTransform)
		}
	}
}
