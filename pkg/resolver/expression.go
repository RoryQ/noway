package resolver

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/roryq/noway/pkg/parser"
)

// EvaluateShouldExecute evaluates a shouldExecute expression after placeholder replacement.
func EvaluateShouldExecute(
	expr string,
	replacer *parser.PlaceholderReplacer,
	builtins parser.BuiltinPlaceholders,
) (bool, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return true, nil
	}

	// 1. Replace placeholders if replacer is provided
	if replacer != nil {
		replaced, err := replacer.Replace(trimmed, builtins)
		if err != nil {
			return false, fmt.Errorf("failed to replace placeholders in shouldExecute expression '%s': %w", expr, err)
		}
		trimmed = strings.TrimSpace(replaced)
	}

	return evalExpr(trimmed)
}

func evalExpr(expr string) (bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true, nil
	}

	// Strip outer parentheses if balanced
	if strings.HasPrefix(expr, "(") && strings.HasSuffix(expr, ")") && isBalanced(expr[1:len(expr)-1]) {
		return evalExpr(expr[1 : len(expr)-1])
	}

	// Handle OR / || (lowest precedence)
	if idx := findLogicalOp(expr, []string{"||", " OR "}); idx != -1 {
		left := expr[:idx]
		var opLen int
		if strings.HasPrefix(expr[idx:], "||") {
			opLen = 2
		} else {
			opLen = 4
		}
		right := expr[idx+opLen:]

		leftVal, err := evalExpr(left)
		if err != nil {
			return false, err
		}
		if leftVal {
			return true, nil // Short circuit
		}
		return evalExpr(right)
	}

	// Handle AND / && (higher precedence than OR)
	if idx := findLogicalOp(expr, []string{"&&", " AND "}); idx != -1 {
		left := expr[:idx]
		var opLen int
		if strings.HasPrefix(expr[idx:], "&&") {
			opLen = 2
		} else {
			opLen = 5
		}
		right := expr[idx+opLen:]

		leftVal, err := evalExpr(left)
		if err != nil {
			return false, err
		}
		if !leftVal {
			return false, nil // Short circuit
		}
		return evalExpr(right)
	}

	// Handle NOT / !
	if strings.HasPrefix(expr, "!") {
		subVal, err := evalExpr(expr[1:])
		return !subVal, err
	}
	if strings.HasPrefix(strings.ToUpper(expr), "NOT ") {
		subVal, err := evalExpr(expr[4:])
		return !subVal, err
	}

	// Handle binary comparisons (==, !=, <=, >=, <, >, =)
	ops := []string{"==", "!=", "<=", ">=", "<", ">", "="}
	for _, op := range ops {
		idx := findComparisonOp(expr, op)
		if idx != -1 {
			leftStr := strings.TrimSpace(expr[:idx])
			rightStr := strings.TrimSpace(expr[idx+len(op):])
			return compareValues(leftStr, rightStr, op)
		}
	}

	// Single boolean literal
	lower := strings.ToLower(expr)
	if lower == "true" || lower == "1" || lower == "yes" {
		return true, nil
	}
	if lower == "false" || lower == "0" || lower == "no" {
		return false, nil
	}

	// Single non-empty unquoted or quoted value is true
	unquoted := unquote(expr)
	if strings.EqualFold(unquoted, "true") {
		return true, nil
	}
	if strings.EqualFold(unquoted, "false") {
		return false, nil
	}

	return false, fmt.Errorf("unable to evaluate boolean expression: %s", expr)
}

func compareValues(left, right, op string) (bool, error) {
	leftVal := unquote(left)
	rightVal := unquote(right)

	// Try boolean comparison
	lBool, lIsBool := parseBool(leftVal)
	rBool, rIsBool := parseBool(rightVal)
	if lIsBool && rIsBool {
		switch op {
		case "==", "=":
			return lBool == rBool, nil
		case "!=":
			return lBool != rBool, nil
		default:
			return false, fmt.Errorf("operator %s not supported for booleans", op)
		}
	}

	// Try numeric comparison
	lNum, lIsNum := parseNumber(leftVal)
	rNum, rIsNum := parseNumber(rightVal)
	if lIsNum && rIsNum {
		switch op {
		case "==", "=":
			return lNum == rNum, nil
		case "!=":
			return lNum != rNum, nil
		case "<":
			return lNum < rNum, nil
		case "<=":
			return lNum <= rNum, nil
		case ">":
			return lNum > rNum, nil
		case ">=":
			return lNum >= rNum, nil
		}
	}

	// String comparison
	switch op {
	case "==", "=":
		return leftVal == rightVal, nil
	case "!=":
		return leftVal != rightVal, nil
	case "<":
		return leftVal < rightVal, nil
	case "<=":
		return leftVal <= rightVal, nil
	case ">":
		return leftVal > rightVal, nil
	case ">=":
		return leftVal >= rightVal, nil
	default:
		return false, fmt.Errorf("unknown comparison operator %s", op)
	}
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if (strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'") && len(s) >= 2) ||
		(strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"") && len(s) >= 2) ||
		(strings.HasPrefix(s, "`") && strings.HasSuffix(s, "`") && len(s) >= 2) {
		return s[1 : len(s)-1]
	}
	return s
}

func parseBool(s string) (bool, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "true" || s == "yes" {
		return true, true
	}
	if s == "false" || s == "no" {
		return false, true
	}
	return false, false
}

func parseNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

func isBalanced(s string) bool {
	depth := 0
	for _, r := range s {
		if r == '(' {
			depth++
		} else if r == ')' {
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

func findLogicalOp(s string, ops []string) int {
	depth := 0
	inQuote := rune(0)

	runes := []rune(s)
	n := len(runes)

	for i := 0; i < n; i++ {
		r := runes[i]
		if inQuote != 0 {
			if r == inQuote {
				inQuote = 0
			}
			continue
		}
		if r == '\'' || r == '"' || r == '`' {
			inQuote = r
			continue
		}
		if r == '(' {
			depth++
			continue
		}
		if r == ')' {
			depth--
			continue
		}

		if depth == 0 {
			sub := string(runes[i:])
			for _, op := range ops {
				if strings.HasPrefix(strings.ToUpper(sub), strings.ToUpper(op)) {
					return i
				}
			}
		}
	}
	return -1
}

func findComparisonOp(s string, op string) int {
	depth := 0
	inQuote := rune(0)

	runes := []rune(s)
	n := len(runes)

	for i := 0; i < n; i++ {
		r := runes[i]
		if inQuote != 0 {
			if r == inQuote {
				inQuote = 0
			}
			continue
		}
		if r == '\'' || r == '"' || r == '`' {
			inQuote = r
			continue
		}
		if r == '(' {
			depth++
			continue
		}
		if r == ')' {
			depth--
			continue
		}

		if depth == 0 {
			sub := string(runes[i:])
			if strings.HasPrefix(sub, op) {
				// Prevent matching "=" inside "==" or "!=" or "<=" or ">="
				if op == "=" {
					if i > 0 && (runes[i-1] == '=' || runes[i-1] == '!' || runes[i-1] == '<' || runes[i-1] == '>') {
						continue
					}
					if i+1 < n && runes[i+1] == '=' {
						continue
					}
				}
				return i
			}
		}
	}
	return -1
}
