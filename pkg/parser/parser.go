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

	// Track recent keyword tokens for block depth
	var lastKeyword string
	var prevKeyword string

	recordKeyword := func(kw string) {
		prevKeyword = lastKeyword
		lastKeyword = strings.ToUpper(kw)

		switch lastKeyword {
		case "BEGIN":
			// If not BEGIN TRANSACTION, increase block depth
			// We check next token or wait
			blockDepth++
		case "TRANSACTION":
			if prevKeyword == "BEGIN" {
				blockDepth--
			}
		case "THEN":
			// In IF ... THEN
			blockDepth++
		case "CASE":
			blockDepth++
		case "END":
			if blockDepth > 0 {
				blockDepth--
			}
		}
	}

	for i < n {
		r := runes[i]

		if r == '\n' {
			currentLine++
		}

		// 1. Check for single line comments: -- or #
		if (r == '-' && peek(1) == '-') || r == '#' {
			// Consume comment until newline or EOF
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

		// 3. Check for triple quotes: ''' or """
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

		// 4. Check for standard string literals: '...' or "..."
		if r == '\'' || r == '"' {
			quote := r
			currentStmt.WriteRune(quote)
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

		// 5. Check for backtick quoted identifier: `...`
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

		// 6. Check for parentheses
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

		// 7. Check for keywords (letters and underscores)
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

		// 8. Check for semicolon statement delimiter
		if r == ';' {
			if parensDepth == 0 && blockDepth == 0 {
				stmtStr := strings.TrimSpace(currentStmt.String())
				if isNonEmptyStatement(stmtStr) {
					statements = append(statements, Statement{
						SQL:        stmtStr,
						LineNumber: stmtStartLine,
					})
				}
				currentStmt.Reset()
				stmtStartLine = currentLine
				lastKeyword = ""
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
	if sql == "" {
		return false
	}
	// Check if it's not just comments or whitespace
	lines := strings.Split(sql, "\n")
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "--") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "/*") && strings.HasSuffix(trimmed, "*/") {
			continue
		}
		return true
	}
	return false
}
