package cautils

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	logger "github.com/kubescape/go-logger"
	"github.com/mikefarah/yq/v4/pkg/yqlib"
	"gopkg.in/op/go-logging.v1"
)

const (
	CommentFormat = `#This is the (?P<line>\d*) line`
)

var apiVersionRe = regexp.MustCompile(`apiVersion: (?P<apiVersion>\S*)`)
var kindRe = regexp.MustCompile(`kind: (?P<kind>\S*)`)
var pathRe = regexp.MustCompile(`path: (?P<path>\S*)`)
var typeRe = regexp.MustCompile(`type: '(?P<type>\S*)'`)
var valueRe = regexp.MustCompile(`value: (?P<value>\[.+\]|\S*)`)
var commentRe = regexp.MustCompile(CommentFormat)
var seqRe = regexp.MustCompile(`.(?P<number>\d+)(?P<point>\.?)`)
var newSeqRe = "[${number}]${point}"

// change to use go func
func GetMapping(fileName string, fileContent string) (*MappingNodes, error) {

	node := new(MappingNode)
	objectID := new(ObjectID)
	mappingNodes := NewMappingNodes()
	mappingNodes.TemplateFileName = fileName

	lines := strings.Split(fileContent, "\n")

	lastNumber := -1
	reducedNumber := -1 // uses to make sure line and line in yq is the same

	isApiVersionEmpty := true
	isKindEmpty := true
	var err error

	var lineExpression = `..| select(line == %d)| {"destpath": path | join("."),"type": type,"value": .}`

	for i, line := range lines {
		index := i
		if apiVersionRe.MatchString(line) {
			isApiVersionEmpty, err = extractApiVersion(line, objectID)
			if err != nil {
				err := fmt.Errorf("extractApiVersion error: err, %s", err.Error())
				return nil, err
			}
			if reducedNumber == -1 {
				reducedNumber = index + reducedNumber
			}
			continue
		} else if kindRe.MatchString(line) {
			isKindEmpty, err = extractKind(line, objectID)
			if err != nil {
				err := fmt.Errorf("extractKind error: err, %s", err.Error())
				return nil, err
			}
			continue
		}

		if isApiVersionEmpty == false || isKindEmpty == false {
			// not sure if it can go to the end
			index = index - reducedNumber
			expression := fmt.Sprintf(lineExpression, index)
			output, err := getYamlLineInfo(expression, fileContent)
			if err != nil {
				err := fmt.Errorf("getYamlLineInfo wrong, the err is %s\n", err.Error())
				return nil, err
			}

			path := extractParameter(pathRe, output, "$path")
			//if path is empty, continue
			if path != "" && path != "\"\"" {
				if isApiVersionEmpty == true || isKindEmpty == true {
					err := fmt.Errorf("There is no enough objectID info")
					return nil, err
				}
				splits := strings.Split(output, "dest")
				if len(splits) < 2 {
					err := fmt.Errorf("Something wrong with the length of the splits, which is %d", len(splits))
					return nil, err
				} else {
					// cut the redundant one
					splits = splits[1:]
					lastNumber, err = writeNodes(splits, lastNumber, fileName, node, objectID, mappingNodes)
					if err != nil {
						err = fmt.Errorf("writeNodes err: %s", err.Error())
						return nil, err
					}
				}
			}

		}
	}
	return mappingNodes, nil
}

func writeNodes(splits []string, lastNumber int, fileName string, node *MappingNode, objectID *ObjectID, mappingNodes *MappingNodes) (int, error) {
	for _, split := range splits {
		path := extractPath(split)
		mapMatched, err := extractMapType(split)
		if err != nil {
			err = fmt.Errorf("extractMapType err: %s", err.Error())
			return -1, err
		}
		if mapMatched {
			lastNumber, err = writeNoteToMapping(split, lastNumber, path, fileName, node, objectID, true, mappingNodes)
			if err != nil {
				err = fmt.Errorf("map type: writeNoteToMapping, err: %s", err.Error())
				return -1, err
			}

		} else {
			lastNumber, err = writeNoteToMapping(split, lastNumber, path, fileName, node, objectID, false, mappingNodes)
			if err != nil {
				err = fmt.Errorf("not map type: writeNoteToMapping, err: %s", err.Error())
				return -1, err
			}
		}
	}
	return lastNumber, nil
}

func writeNoteToMapping(split string, lastNumber int, path string, fileName string, node *MappingNode, objectID *ObjectID, isMapType bool, mappingNodes *MappingNodes) (int, error) {
	newlastNumber, err := writeNodeInfo(split, lastNumber, path, fileName, node, objectID, isMapType)
	if err != nil {
		err = fmt.Errorf("isMapType: %v, writeNodeInfo wrong err: %s", isMapType, err.Error())
		return 0, err
	}
	if _, ok := mappingNodes.Nodes[path]; !ok {
		mappingNodes.Nodes[path] = *node
	} else {
		err = fmt.Errorf("isMapType: %v, %s in mapping.Nodes exists, err: %s", isMapType, path, err.Error())
		return 0, err
	}
	return newlastNumber, nil
}

func writeNodeInfo(split string, lastNumber int, path string, fileName string, node *MappingNode, objectID *ObjectID, isMapType bool) (int, error) {
	value, lineNumber, newLastNumber, err := getInfoFromOne(split, lastNumber, isMapType)
	if err != nil {
		err = fmt.Errorf("getInfoFromOne wrong err: %s", err.Error())
		return -1, err
	}
	// lastNumber = newLastNumber
	node.writeInfoToNode(objectID, path, lineNumber, value, fileName)
	return newLastNumber, nil
}

func getInfoFromOne(output string, lastNumber int, isMapType bool) (value string, lineNumber int, newLastNumber int, err error) {
	if isMapType == true {
		value = ""
	} else {
		value = extractParameter(valueRe, output, "$value")
	}
	number := extractParameter(commentRe, output, "$line")
	if number != "" {
		lineNumber, err = strconv.Atoi(number)
		if err != nil {
			err = fmt.Errorf("strconv.Atoi err: %s", err.Error())
			return "", -1, -1, err
		}
		if isMapType == true {
			lineNumber = lineNumber - 1
		}
		lastNumber = lineNumber
		// save to structure
	} else {
		lineNumber = lastNumber
		// use the last one number
	}
	newLastNumber = lineNumber
	return value, lineNumber, newLastNumber, nil
}

func getYamlLineInfo(expression string, yamlFile string) (string, error) {
	out, err := exectuateYq(expression, yamlFile)
	if err != nil {
		err = fmt.Errorf("exectuateYq err: %s", err.Error())
		return "", err
	}
	return out, nil
}

func exectuateYq(expression string, yamlContent string) (string, error) {

	backendLoggerLeveled := logging.AddModuleLevel(logging.NewLogBackend(logger.L().GetWriter(), "", 0))
	backendLoggerLeveled.SetLevel(logging.ERROR, "")
	yqlib.GetLogger().SetBackend(backendLoggerLeveled)

	encoder := configureEncoder()

	decoder := configureDecoder(false)

	stringEvaluator := yqlib.NewStringEvaluator()

	out, err := stringEvaluator.Evaluate(expression, yamlContent, encoder, decoder)
	if err != nil {
		return "", errors.New("no matches found")
	}
	return out, err
}

func extractApiVersion(line string, objectID *ObjectID) (bool, error) {
	apiVersion := extractParameter(apiVersionRe, line, "$apiVersion")
	if apiVersion == "" {
		err := fmt.Errorf("Something wrong when extracting the apiVersion, the line is %s\n", line)
		return true, err
	}
	objectID.apiVersion = apiVersion
	return false, nil
}

func extractKind(line string, objectID *ObjectID) (bool, error) {
	kind := extractParameter(kindRe, line, "$kind")
	if kind == "" {
		err := fmt.Errorf("Something wrong when extracting the kind, the line is %s\n", line)
		return true, err
	}
	objectID.kind = kind
	return false, nil
}
func extractPath(split string) string {
	path := extractParameter(pathRe, split, "$path")
	// For each match of the regex in the content.
	path = seqRe.ReplaceAllString(path, newSeqRe)
	return path
}

func extractMapType(split string) (bool, error) {
	pathType := extractParameter(typeRe, split, "$type")
	mapMatched, err := regexp.MatchString(`!!map`, pathType)
	if err != nil {
		err = fmt.Errorf("regexp.MatchString err: %s", err.Error())
		return false, err
	}
	return mapMatched, nil
}

func extractParameter(re *regexp.Regexp, line string, keyword string) string {
	submatch := re.FindStringSubmatchIndex(line)
	result := []byte{}
	result = re.ExpandString(result, keyword, line, submatch)
	parameter := string(result)
	return parameter
}

//yqlib configuration

func configurePrinterWriter(out io.Writer) yqlib.PrinterWriter {
	var printerWriter yqlib.PrinterWriter
	printerWriter = yqlib.NewSinglePrinterWriter(out)

	return printerWriter
}

func configureEncoder() yqlib.Encoder {
	indent := 2
	colorsEnabled := false
	yqlibEncoder := yqlib.NewYamlEncoder(indent, colorsEnabled, yqlib.ConfiguredYamlPreferences)
	return yqlibEncoder
}

func configureDecoder(evaluateTogether bool) yqlib.Decoder {
	prefs := yqlib.ConfiguredYamlPreferences
	prefs.EvaluateTogether = evaluateTogether
	yqlibDecoder := yqlib.NewYamlDecoder(prefs)
	return yqlibDecoder
}