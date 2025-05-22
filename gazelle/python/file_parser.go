// Copyright 2023 The Bazel Authors. All rights reserved.
//
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

package python

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"regexp"
)

const (
	sitterNodeTypeString              = "string"
	sitterNodeTypeComment             = "comment"
	sitterNodeTypeIdentifier          = "identifier"
	sitterNodeTypeDottedName          = "dotted_name"
	sitterNodeTypeIfStatement         = "if_statement"
	sitterNodeTypeAliasedImport       = "aliased_import"
	sitterNodeTypeWildcardImport      = "wildcard_import"
	sitterNodeTypeImportStatement     = "import_statement"
	sitterNodeTypeComparisonOperator  = "comparison_operator"
	sitterNodeTypeImportFromStatement = "import_from_statement"
)

type ParserOutput struct {
	FileName string
	Modules  []module
	Comments []comment
	HasMain  bool
}

type FileParser struct {
	code        []byte
	relFilepath string
	output      ParserOutput
}

func NewFileParser() *FileParser {
	return &FileParser{}
}


func (p *FileParser) SetCodeAndFile(code []byte, relPackagePath, filename string) {
	p.code = code
	p.relFilepath = filepath.Join(relPackagePath, filename)
	p.output.FileName = filename
}


// func (p *FileParser) Parse(ctx context.Context) (*ParserOutput, error) {
// 	rootNode, err := ParseCode(p.code, p.relFilepath)
// 	if err != nil {
// 		return nil, err
// 	}

// 	p.output.HasMain = p.parseMain(ctx, rootNode)

// 	p.parse(ctx, rootNode)
// 	return &p.output, nil
// }

func (p *FileParser) Parse(ctx context.Context) (*ParserOutput, error) {
	return p.naiveFileParser(p.code)
}

func (p *FileParser) ParseFile(ctx context.Context, repoRoot, relPackagePath, filename string) (*ParserOutput, error) {
	code, err := os.ReadFile(filepath.Join(repoRoot, relPackagePath, filename))
	if err != nil {
		return nil, err
	}
	p.SetCodeAndFile(code, relPackagePath, filename)
	return p.Parse(ctx)
}

// file_parser.go

// A really really dumb python parser that's "good enough". Hopefully.
func (p *FileParser) naiveFileParser(code []byte) (*ParserOutput, error) {
	codeStr := string(code[:])

	// Parse imports
	modules, err := p.naiveImportParser(codeStr)
	if err != nil {
		return nil, err
	}
	p.output.Modules = modules

	// Parse comments
	// TODO: find comment-in-string or comment-in-comment cases
	commentPattern := regexp.MustCompile(`(?m)#.+$`)
	commentStrings := commentPattern.FindAllString(codeStr, -1)
	var comments []comment
	for _, s := range commentStrings {
		comments = append(comments, comment(s))
	}
	p.output.Comments = comments

	// Parse 'if __name__' block - only at root level (no indentation)
	// Support both single and double quotes
	mainPattern := regexp.MustCompile(`(?m)^if\s+__name__\s*==\s*["']__main__["']\s*:`)
	p.output.HasMain = mainPattern.MatchString(codeStr)
	return &p.output, nil
}


// parseImportLine processes a single import line and returns any modules found
func (p *FileParser) parseImportLine(line string, lineNum uint32) []module {
	trimmed := strings.TrimSpace(line)
	// Skip empty lines or full-line comments
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return nil
	}
	// Handle import statements
	if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "from ") {
		return parseImportStatement(trimmed, lineNum, p.relFilepath)
	}
	return nil
}

// ... existing code ...

// isMultilineImportStart checks if a line starts a multiline import
func isMultilineImportStart(line string) bool {
	return strings.HasSuffix(line, "\\") || strings.Contains(line, "(")
}

// isMultilineImportEnd checks if a line ends a multiline import
func isMultilineImportEnd(line string) bool {
	return strings.Contains(line, ")") || (!strings.HasSuffix(line, "\\") && !strings.Contains(line, "("))
}

func (p *FileParser) naiveImportParser(code string) ([]module, error) {
	lines := strings.Split(strings.ReplaceAll(code, "\r\n", "\n"), "\n")
	var modules []module
	var currentImportLines []string
	var inMultilineImport bool
	var startLineNum int
	var inParentheses bool

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Handle multiline imports
		if isMultilineImportStart(trimmed) || inMultilineImport {
			if !inMultilineImport {
				startLineNum = i
				inParentheses = strings.Contains(trimmed, "(")
			}
			currentImportLines = append(currentImportLines, trimmed)
			inMultilineImport = true

			// Check for end of multiline import
			if inParentheses {
				if strings.Contains(trimmed, ")") {
					inMultilineImport = false
					inParentheses = false
					joined := strings.Join(currentImportLines, " ")
					currentImportLines = nil

					mods := parseImportStatement(joined, uint32(startLineNum), p.relFilepath)
					modules = append(modules, mods...)
				}
			} else if !strings.HasSuffix(trimmed, "\\") {
				inMultilineImport = false
				joined := strings.Join(currentImportLines, " ")
				currentImportLines = nil

				mods := parseImportStatement(joined, uint32(startLineNum), p.relFilepath)
				modules = append(modules, mods...)
			}
			continue
		}

		// Handle single line imports
		if mods := p.parseImportLine(trimmed, uint32(i)); mods != nil {
			modules = append(modules, mods...)
		}
	}

	return modules, nil
}

// parseImportStatement parses a single import statement and returns the modules it imports
func parseImportStatement(statement string, lineNum uint32, filepath string) []module {
	// Remove inline comment and clean up whitespace
	statement, _, _ = strings.Cut(statement, "#")
	statement = strings.TrimSpace(statement)

	// Remove parentheses and clean up whitespace
	statement = strings.Trim(statement, "() ")
	statement = strings.TrimSpace(statement)

	if strings.HasPrefix(statement, "from ") {
		return parseFromImport(statement[5:], lineNum, filepath)
	} else if strings.HasPrefix(statement, "import ") {
		return parseDirectImport(statement[7:], lineNum, filepath)
	}

	return nil
}

// parseFromImport handles "from X import Y" statements
func parseFromImport(statement string, lineNum uint32, filepath string) []module {
	fromPart, importPart, found := strings.Cut(statement, " import ")
	if !found {
		return nil
	}

	fromPart = strings.TrimSpace(fromPart)
	importPart = strings.Trim(importPart, "() ")
	var mods []module

	for _, item := range strings.Split(importPart, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		// Ignore aliasing (i.e., "as x")
		item, _, _ = strings.Cut(item, " as ")
		moduleName := fmt.Sprintf("%s.%s", fromPart, item)
		if strings.HasPrefix(fromPart, ".") {
			continue
		}
		mods = append(mods, module{
			Name:       moduleName,
			LineNumber: lineNum + 1,
			Filepath:   filepath,
			From:       fromPart,
		})
	}

	return mods
}

// parseDirectImport handles "import X" statements
func parseDirectImport(statement string, lineNum uint32, filepath string) []module {
	var mods []module
	importPart := strings.TrimSpace(statement)
	importPart = strings.Trim(importPart, "() ")

	for _, item := range strings.Split(importPart, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		item, _, _ = strings.Cut(item, " as ")
		mods = append(mods, module{
			Name:       item,
			LineNumber: lineNum + 1,
			Filepath:   filepath,
			From:       "",
		})
	}

	return mods
}
