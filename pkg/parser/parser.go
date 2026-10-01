package parser

import (
	"strings"
	"unicode"
)

// Statement represents a parsed SQL statement.
type Statement struct {
	SQL        string
	LineNumber int
}

// BigQueryParser parses and splits BigQuery Standard SQL scripts into executable statements.
type BigQueryParser struct{}

// NewBigQueryParser creates a new BigQueryParser.
func NewBigQueryParser() *BigQueryParser {
	return &BigQueryParser{}
}

// SplitStatements splits a BigQuery SQL script into individual statements.
func (p *BigQueryParser) SplitStatements(script string) []Statement {
	var statements []Statement
	var currentStmt strings.Builder

	runes := []rune(script)
	n := len(runes)
	i := 0
	currentLine := 1
	stmtStartLine := 1

	blockDepth := 0
	caseDepth := 0
	parensDepth := 0

	// Helper to peek ahead
	peek := func(offset int) rune {
		if i+offset < n {
			return runes[i+offset]
		}
		return 0
	}

	// Helper to peek string
	peekStr := func(length int) string {
		if i+length <= n {
			return string(runes[i : i+length])
		}
		return ""
	}

	// Track previous non-whitespace keyword tokens in current statement
	var prevKeyword string

	recordKeyword := func(kw string) {
		upper := strings.ToUpper(kw)

		switch upper {
		case "BEGIN":
			blockDepth++
		case "TRANSACTION":
			// If preceded by BEGIN in this statement, cancel block depth
			if prevKeyword == "BEGIN" && blockDepth > 0 {
				blockDepth--
			}
		case "CASE":
			caseDepth++
		case "IF":
			// Check if this is a procedural IF statement
			// NOT procedural if preceded by TABLE, VIEW, SCHEMA, INDEX, DROP, CREATE, END, ELSE, etc.
			isNotProcedural := prevKeyword == "TABLE" || prevKeyword == "VIEW" || prevKeyword == "SCHEMA" ||
				prevKeyword == "INDEX" || prevKeyword == "DROP" || prevKeyword == "CREATE" ||
				prevKeyword == "FUNCTION" || prevKeyword == "PROCEDURE" || prevKeyword == "OR" ||
				prevKeyword == "END" || prevKeyword == "ELSE"
			if !isNotProcedural {
				blockDepth++
			}
		case "LOOP", "WHILE", "REPEAT":
			if prevKeyword != "END" {
				blockDepth++
			}
		case "FOR":
			// Procedural FOR record IN (...) DO ... END FOR
			if prevKeyword != "END" && prevKeyword != "CREATE" && prevKeyword != "REPLACE" {
				blockDepth++
			}
		case "END":
			if caseDepth > 0 {
				caseDepth--
			} else if blockDepth > 0 {
				blockDepth--
			}
		}

		prevKeyword = upper
	}

	for i < n {
		r := runes[i]

		if r == '\n' {
			currentLine++
		}

		// 1. Check for single line comments: -- or #
		if (r == '-' && peek(1) == '-') || r == '#' {
			for i < n && runes[i] != '\n' {
				currentStmt.WriteRune(runes[i])
				i++
			}
			if i < n && runes[i] == '\n' {
				currentStmt.WriteRune('\n')
				currentLine++
				i++
			}
			continue
		}

		// 2. Check for multi-line comments: /* ... */
		if r == '/' && peek(1) == '*' {
			currentStmt.WriteRune(r)
			currentStmt.WriteRune(peek(1))
			i += 2
			for i < n {
				if runes[i] == '\n' {
					currentLine++
				}
				if runes[i] == '*' && peek(1) == '/' {
					currentStmt.WriteRune('*')
					currentStmt.WriteRune('/')
					i += 2
					break
				}
				currentStmt.WriteRune(runes[i])
				i++
			}
			continue
		}

		// 3. Check for raw/byte string prefix: r', r", b', b", rb', etc.
		if (r == 'r' || r == 'R' || r == 'b' || r == 'B') && (peek(1) == '\'' || peek(1) == '"') {
			currentStmt.WriteRune(r)
			i++
			r = runes[i]
		}

		// 4. Check for triple quotes: ''' or """
		if (r == '\'' && peekStr(3) == "'''") || (r == '"' && peekStr(3) == `"""`) {
			quoteStr := peekStr(3)
			currentStmt.WriteString(quoteStr)
			i += 3
			for i < n {
				if runes[i] == '\n' {
					currentLine++
				}
				if peekStr(3) == quoteStr {
					currentStmt.WriteString(quoteStr)
					i += 3
					break
				}
				if runes[i] == '\\' && i+1 < n {
					currentStmt.WriteRune(runes[i])
					i++
					currentStmt.WriteRune(runes[i])
					i++
					continue
				}
				currentStmt.WriteRune(runes[i])
				i++
			}
			continue
		}

		// 5. Check for standard string literals: '...' or "..." (with \' and doubled '' escaping)
		if r == '\'' || r == '"' {
			quote := r
			currentStmt.WriteRune(quote)
			i++
			for i < n {
				if runes[i] == '\n' {
					currentLine++
				}
				// Escaped by backslash: \' or \"
				if runes[i] == '\\' && i+1 < n {
					currentStmt.WriteRune(runes[i])
					i++
					currentStmt.WriteRune(runes[i])
					i++
					continue
				}
				// Escaped by doubling: '' or ""
				if runes[i] == quote && i+1 < n && runes[i+1] == quote {
					currentStmt.WriteRune(quote)
					currentStmt.WriteRune(quote)
					i += 2
					continue
				}
				if runes[i] == quote {
					currentStmt.WriteRune(quote)
					i++
					break
				}
				currentStmt.WriteRune(runes[i])
				i++
			}
			continue
		}

		// 6. Check for backtick quoted identifier: `...`
		if r == '`' {
			currentStmt.WriteRune('`')
			i++
			for i < n {
				if runes[i] == '\n' {
					currentLine++
				}
				if runes[i] == '\\' && i+1 < n {
					currentStmt.WriteRune(runes[i])
					i++
					currentStmt.WriteRune(runes[i])
					i++
					continue
				}
				if runes[i] == '`' {
					currentStmt.WriteRune('`')
					i++
					break
				}
				currentStmt.WriteRune(runes[i])
				i++
			}
			continue
		}

		// 7. Check for parentheses
		if r == '(' {
			parensDepth++
			currentStmt.WriteRune(r)
			i++
			continue
		}
		if r == ')' {
			if parensDepth > 0 {
				parensDepth--
			}
			currentStmt.WriteRune(r)
			i++
			continue
		}

		// 8. Check for identifiers and keywords
		if unicode.IsLetter(r) || r == '_' {
			start := i
			for i < n && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) || runes[i] == '_') {
				currentStmt.WriteRune(runes[i])
				i++
			}
			word := string(runes[start:i])
			recordKeyword(word)
			continue
		}

		// 9. Check for semicolon statement delimiter
		if r == ';' {
			if parensDepth == 0 && blockDepth == 0 && caseDepth == 0 {
				stmtStr := strings.TrimSpace(currentStmt.String())
				if isNonEmptyStatement(stmtStr) {
					statements = append(statements, Statement{
						SQL:        stmtStr,
						LineNumber: stmtStartLine,
					})
				}
				currentStmt.Reset()
				stmtStartLine = currentLine
				prevKeyword = ""
				i++
				continue
			} else {
				currentStmt.WriteRune(r)
				i++
				continue
			}
		}

		if currentStmt.Len() == 0 && !unicode.IsSpace(r) {
			stmtStartLine = currentLine
		}
		currentStmt.WriteRune(r)
		i++
	}

	remaining := strings.TrimSpace(currentStmt.String())
	if isNonEmptyStatement(remaining) {
		statements = append(statements, Statement{
			SQL:        remaining,
			LineNumber: stmtStartLine,
		})
	}

	return statements
}

func isNonEmptyStatement(sql string) bool {
	trimmed := strings.TrimSpace(sql)
	if trimmed == "" {
		return false
	}

	// Strip single-line comments (-- and #) and multi-line comments (/* ... */)
	runes := []rune(trimmed)
	n := len(runes)
	var sb strings.Builder
	i := 0

	for i < n {
		r := runes[i]
		if (r == '-' && i+1 < n && runes[i+1] == '-') || r == '#' {
			for i < n && runes[i] != '\n' {
				i++
			}
			continue
		}
		if r == '/' && i+1 < n && runes[i+1] == '*' {
			i += 2
			for i < n {
				if runes[i] == '*' && i+1 < n && runes[i+1] == '/' {
					i += 2
					break
				}
				i++
			}
			continue
		}
		if !unicode.IsSpace(r) {
			sb.WriteRune(r)
		}
		i++
	}

	return sb.Len() > 0
}
