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

	parensDepth := 0
	var blockStack []string

	pushBlock := func(kind string) {
		blockStack = append(blockStack, kind)
	}

	popBlock := func(kind string) {
		for idx := len(blockStack) - 1; idx >= 0; idx-- {
			if blockStack[idx] == kind {
				blockStack = blockStack[:idx]
				return
			}
		}
	}

	popTopBlock := func() {
		if len(blockStack) == 0 {
			return
		}
		top := blockStack[len(blockStack)-1]
		if top == "BEGIN" || top == "CASE" {
			blockStack = blockStack[:len(blockStack)-1]
		} else {
			for idx := len(blockStack) - 1; idx >= 0; idx-- {
				if blockStack[idx] == "BEGIN" || blockStack[idx] == "CASE" {
					blockStack = blockStack[:idx]
					return
				}
			}
			blockStack = blockStack[:len(blockStack)-1]
		}
	}

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

	skipWhitespaceAndComments := func(pos int) int {
		k := pos
		for k < n {
			if unicode.IsSpace(runes[k]) {
				k++
				continue
			}
			// Single-line comment: -- or #
			if (runes[k] == '-' && k+1 < n && runes[k+1] == '-') || runes[k] == '#' {
				k++
				for k < n && runes[k] != '\n' {
					k++
				}
				continue
			}
			// Multi-line comment: /* ... */
			if runes[k] == '/' && k+1 < n && runes[k+1] == '*' {
				k += 2
				for k < n {
					if runes[k] == '*' && k+1 < n && runes[k+1] == '/' {
						k += 2
						break
					}
					k++
				}
				continue
			}
			break
		}
		return k
	}

	isNextTokenParen := func(pos int) bool {
		k := skipWhitespaceAndComments(pos)
		return k < n && runes[k] == '('
	}

	getNextWord := func(pos int) string {
		k := skipWhitespaceAndComments(pos)
		start := k
		for k < n && (unicode.IsLetter(runes[k]) || unicode.IsDigit(runes[k]) || runes[k] == '_') {
			k++
		}
		if k > start {
			return string(runes[start:k])
		}
		return ""
	}

	isNextWordSystemTime := func(pos int) bool {
		return strings.EqualFold(getNextWord(pos), "SYSTEM_TIME")
	}

	isScalarIF := func(pos int) bool {
		k := skipWhitespaceAndComments(pos)
		if k >= n || runes[k] != '(' {
			return false
		}

		pDepth := 0
		hasTopLevelComma := false
		matchClosePos := -1

		scanPos := k
		for scanPos < n {
			r := runes[scanPos]

			// Single-line comment: -- or #
			if (r == '-' && scanPos+1 < n && runes[scanPos+1] == '-') || r == '#' {
				scanPos++
				for scanPos < n && runes[scanPos] != '\n' {
					scanPos++
				}
				continue
			}

			// Multi-line comment: /* ... */
			if r == '/' && scanPos+1 < n && runes[scanPos+1] == '*' {
				scanPos += 2
				for scanPos < n {
					if runes[scanPos] == '*' && scanPos+1 < n && runes[scanPos+1] == '/' {
						scanPos += 2
						break
					}
					scanPos++
				}
				continue
			}

			// Triple quotes: ''' or """
			if (r == '\'' && scanPos+2 < n && runes[scanPos+1] == '\'' && runes[scanPos+2] == '\'') ||
				(r == '"' && scanPos+2 < n && runes[scanPos+1] == '"' && runes[scanPos+2] == '"') {
				q := runes[scanPos]
				scanPos += 3
				for scanPos < n {
					if runes[scanPos] == q && scanPos+2 < n && runes[scanPos+1] == q && runes[scanPos+2] == q {
						scanPos += 3
						break
					}
					if runes[scanPos] == '\\' && scanPos+1 < n {
						scanPos += 2
						continue
					}
					scanPos++
				}
				continue
			}

			// Standard string literals or backticks: '...', "...", `...`
			if r == '\'' || r == '"' || r == '`' {
				quote := r
				scanPos++
				for scanPos < n {
					if runes[scanPos] == '\\' && scanPos+1 < n {
						scanPos += 2
						continue
					}
					if runes[scanPos] == quote {
						if scanPos+1 < n && runes[scanPos+1] == quote {
							scanPos += 2
							continue
						}
						scanPos++
						break
					}
					scanPos++
				}
				continue
			}

			// Parentheses tracking
			if r == '(' {
				pDepth++
			} else if r == ')' {
				pDepth--
				if pDepth == 0 {
					matchClosePos = scanPos + 1
					break
				}
			} else if r == ',' && pDepth == 1 {
				hasTopLevelComma = true
			} else if r == ';' && pDepth == 0 {
				break
			}
			scanPos++
		}

		if hasTopLevelComma {
			return true
		}

		// If no top-level comma, check if THEN appears after closing ')' before ';'
		if matchClosePos != -1 {
			afterPos := matchClosePos
			for afterPos < n {
				afterPos = skipWhitespaceAndComments(afterPos)
				if afterPos >= n || runes[afterPos] == ';' {
					break
				}
				r := runes[afterPos]
				if r == '\'' || r == '"' || r == '`' {
					quote := r
					afterPos++
					for afterPos < n {
						if runes[afterPos] == '\\' && afterPos+1 < n {
							afterPos += 2
							continue
						}
						if runes[afterPos] == quote {
							if afterPos+1 < n && runes[afterPos+1] == quote {
								afterPos += 2
								continue
							}
							afterPos++
							break
						}
						afterPos++
					}
					continue
				}
				if unicode.IsLetter(r) || r == '_' {
					wStart := afterPos
					for afterPos < n && (unicode.IsLetter(runes[afterPos]) || unicode.IsDigit(runes[afterPos]) || runes[afterPos] == '_') {
						afterPos++
					}
					w := string(runes[wStart:afterPos])
					if strings.EqualFold(w, "THEN") {
						return false
					}
					continue
				}
				afterPos++
			}
		}

		return true
	}

	recordKeyword := func(kw string, pos int) {
		upper := strings.ToUpper(kw)

		switch upper {
		case "BEGIN":
			if parensDepth == 0 {
				pushBlock("BEGIN")
			}
		case "TRANSACTION":
			// If preceded by BEGIN in this statement, cancel block depth
			if prevKeyword == "BEGIN" && len(blockStack) > 0 && blockStack[len(blockStack)-1] == "BEGIN" {
				blockStack = blockStack[:len(blockStack)-1]
			}
		case "CASE":
			if prevKeyword != "END" {
				pushBlock("CASE")
			}
		case "IF":
			// Procedural IF vs DDL IF [NOT] EXISTS vs scalar IF(...)
			isDDLOrClosing := prevKeyword == "TABLE" || prevKeyword == "VIEW" || prevKeyword == "SCHEMA" ||
				prevKeyword == "DATASET" || prevKeyword == "INDEX" || prevKeyword == "DROP" ||
				prevKeyword == "CREATE" || prevKeyword == "FUNCTION" || prevKeyword == "PROCEDURE" ||
				prevKeyword == "COLUMN" || prevKeyword == "POLICY" || prevKeyword == "CONSTRAINT" ||
				prevKeyword == "KEY" || prevKeyword == "OR" || prevKeyword == "END"

			isScalarOrExpr := parensDepth > 0 || isScalarIF(pos)

			if !isDDLOrClosing && !isScalarOrExpr {
				pushBlock("IF")
			}
		case "LOOP", "WHILE":
			if prevKeyword != "END" && parensDepth == 0 {
				pushBlock(upper)
			}
		case "REPEAT":
			// Scalar REPEAT(str, n) vs procedural REPEAT ... UNTIL ... END REPEAT
			if prevKeyword != "END" && parensDepth == 0 && !isNextTokenParen(pos) {
				pushBlock("REPEAT")
			}
		case "FOR":
			// Procedural FOR record IN (...) DO ... END FOR vs FOR SYSTEM_TIME AS OF ...
			if prevKeyword != "END" && prevKeyword != "CREATE" && prevKeyword != "REPLACE" &&
				parensDepth == 0 && !isNextWordSystemTime(pos) {
				pushBlock("FOR")
			}
		case "END":
			nextWord := strings.ToUpper(getNextWord(pos))
			switch nextWord {
			case "CASE":
				popBlock("CASE")
			case "IF", "LOOP", "WHILE", "REPEAT", "FOR":
				popBlock(nextWord)
			default:
				popTopBlock()
			}
		}

		prevKeyword = upper
	}

	for i < n {
		r := runes[i]

		if r == '\n' {
			currentLine++
		}

		if currentStmt.Len() == 0 && unicode.IsSpace(r) {
			i++
			continue
		}

		if currentStmt.Len() == 0 {
			stmtStartLine = currentLine
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

		// 3. Check for raw/byte string prefix: r', r", b', b", rb', br', etc.
		if r == 'r' || r == 'R' || r == 'b' || r == 'B' {
			next := peek(1)
			if next == '\'' || next == '"' {
				currentStmt.WriteRune(r)
				i++
				r = runes[i]
			} else if ((r == 'r' || r == 'R') && (next == 'b' || next == 'B')) ||
				((r == 'b' || r == 'B') && (next == 'r' || next == 'R')) {
				next2 := peek(2)
				if next2 == '\'' || next2 == '"' {
					currentStmt.WriteRune(r)
					currentStmt.WriteRune(next)
					i += 2
					r = runes[i]
				}
			}
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
			prevKeyword = ""
			currentStmt.WriteRune(r)
			i++
			continue
		}
		if r == ')' {
			if parensDepth > 0 {
				parensDepth--
			}
			prevKeyword = ""
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
			recordKeyword(word, i)
			continue
		}

		// 9. Check for semicolon statement delimiter
		if r == ';' {
			prevKeyword = ""
			if parensDepth == 0 && len(blockStack) == 0 {
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
				blockStack = blockStack[:0]
				i++
				continue
			} else {
				currentStmt.WriteRune(r)
				i++
				continue
			}
		}

		if !unicode.IsSpace(r) {
			prevKeyword = ""
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
