package format

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"strconv"
)

var MatrixToURL = regexp.MustCompile("^(?:https?://)?(?:www\\.)?matrix\\.to/#/([#@!+].*)(?:/(\\$.+))?")

type TextConverter func(string) string

type HTMLParser struct {
	PillConverter           func(mxid, eventID string) string
	TabsToSpaces            int
	Newline                 string
	BoldConverter           TextConverter
	ItalicConverter         TextConverter
	StrikethroughConverter  TextConverter
	UnderlineConverter      TextConverter
	MonospaceBlockConverter TextConverter
	MonospaceConverter      TextConverter
}

type TaggedString struct {
	string
	tag string
}

func (parser *HTMLParser) getAttribute(node *html.Node, attribute string) string {
	for _, attr := range node.Attr {
		if attr.Key == attribute {
			return attr.Val
		}
	}
	return ""
}

func Digits(num int) int {
	return int(math.Floor(math.Log10(float64(num))) + 1)
}

func (parser *HTMLParser) listToString(node *html.Node, stripLinebreak bool) string {
	ordered := node.Data == "ol"
	taggedChildren := parser.nodeToTaggedStrings(node.FirstChild, stripLinebreak)
	counter := 1
	indentLength := 0
	if ordered {
		start := parser.getAttribute(node, "start")
		if len(start) > 0 {
			counter, _ = strconv.Atoi(start)
		}

		longestIndex := (counter - 1) + len(taggedChildren)
		indentLength = Digits(longestIndex)
	}
	indent := strings.Repeat(" ", indentLength+2)
	var children []string
	for _, child := range taggedChildren {
		if child.tag != "li" {
			continue
		}
		var prefix string
		if ordered {
			indexPadding := indentLength - Digits(counter)
			prefix = fmt.Sprintf("%d. %s", counter, strings.Repeat(" ", indexPadding))
		} else {
			prefix = "● "
		}
		str := prefix + child.string
		counter++
		parts := strings.Split(str, "\n")
		for i, part := range parts[1:] {
			parts[i+1] = indent + part
		}
		str = strings.Join(parts, "\n")
		children = append(children, str)
	}
	return strings.Join(children, "\n")
}

func (parser *HTMLParser) basicFormatToString(node *html.Node, stripLinebreak bool) string {
	str := parser.nodeToTagAwareString(node.FirstChild, stripLinebreak)
	switch node.Data {
	case "b", "strong":
		if parser.BoldConverter != nil {
			return parser.BoldConverter(str)
		}
		return fmt.Sprintf("**%s**", str)
	case "i", "em":
		if parser.ItalicConverter != nil {
			return parser.ItalicConverter(str)
		}
		return fmt.Sprintf("_%s_", str)
	case "s", "del":
		if parser.StrikethroughConverter != nil {
			return parser.StrikethroughConverter(str)
		}
		return fmt.Sprintf("~~%s~~", str)
	case "u", "ins":
		if parser.UnderlineConverter != nil {
			return parser.UnderlineConverter(str)
		}
	case "tt", "code":
		if parser.MonospaceConverter != nil {
			return parser.MonospaceConverter(str)
		}
	}
	return str
}

func (parser *HTMLParser) headerToString(node *html.Node, stripLinebreak bool) string {
	children := parser.nodeToStrings(node.FirstChild, stripLinebreak)
	length := int(node.Data[1] - '0')
	prefix := strings.Repeat("#", length) + " "
	return prefix + strings.Join(children, "")
}

func (parser *HTMLParser) blockquoteToString(node *html.Node, stripLinebreak bool) string {
	str := parser.nodeToTagAwareString(node.FirstChild, stripLinebreak)
	childrenArr := strings.Split(strings.TrimSpace(str), "\n")
	for index, child := range childrenArr {
		childrenArr[index] = "> " + child
	}
	return strings.Join(childrenArr, "\n")
}

func (parser *HTMLParser) linkToString(node *html.Node, stripLinebreak bool) string {
	str := parser.nodeToTagAwareString(node.FirstChild, stripLinebreak)
	href := parser.getAttribute(node, "href")
	if len(href) == 0 {
		return str
	}
	match := MatrixToURL.FindStringSubmatch(href)
	if len(match) == 2 || len(match) == 3 {
		if parser.PillConverter != nil {
			mxid := match[1]
			eventID := ""
			if len(match) == 3 {
				eventID = match[2]
			}
			return parser.PillConverter(mxid, eventID)
		}
		return str
	}
	return fmt.Sprintf("%s (%s)", str, href)
}

func (parser *HTMLParser) tagToString(node *html.Node, stripLinebreak bool) string {
	switch node.Data {
	case "blockquote":
		return parser.blockquoteToString(node, stripLinebreak)
	case "ol", "ul":
		return parser.listToString(node, stripLinebreak)
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return parser.headerToString(node, stripLinebreak)
	case "br":
		return parser.Newline
	case "b", "strong", "i", "em", "s", "del", "u", "ins", "tt", "code":
		return parser.basicFormatToString(node, stripLinebreak)
	case "a":
		return parser.linkToString(node, stripLinebreak)
	case "p":
		return parser.nodeToTagAwareString(node.FirstChild, stripLinebreak) + "\n"
	case "pre":
		var preStr string
		if node.FirstChild != nil && node.FirstChild.Type == html.ElementNode && node.FirstChild.Data == "code" {
			preStr = parser.nodeToString(node.FirstChild.FirstChild, false)
		} else {
			preStr = parser.nodeToString(node.FirstChild, false)
		}
		if parser.MonospaceBlockConverter != nil {
			return parser.MonospaceBlockConverter(preStr)
		}
		return preStr
	default:
		return parser.nodeToTagAwareString(node.FirstChild, stripLinebreak)
	}
}

func (parser *HTMLParser) singleNodeToString(node *html.Node, stripLinebreak bool) TaggedString {
	switch node.Type {
	case html.TextNode:
		if stripLinebreak {
			node.Data = strings.Replace(node.Data, "\n", "", -1)
		}
		return TaggedString{node.Data, "text"}
	case html.ElementNode:
		return TaggedString{parser.tagToString(node, stripLinebreak), node.Data}
	case html.DocumentNode:
		return TaggedString{parser.nodeToTagAwareString(node.FirstChild, stripLinebreak), "html"}
	default:
		return TaggedString{"", "unknown"}
	}
}

func (parser *HTMLParser) nodeToTaggedStrings(node *html.Node, stripLinebreak bool) (strs []TaggedString) {
	for ; node != nil; node = node.NextSibling {
		strs = append(strs, parser.singleNodeToString(node, stripLinebreak))
	}
	return
}

var BlockTags = []string{"p", "h1", "h2", "h3", "h4", "h5", "h6", "ol", "ul", "pre", "blockquote", "div", "hr", "table"}

func (parser *HTMLParser) isBlockTag(tag string) bool {
	for _, blockTag := range BlockTags {
		if tag == blockTag {
			return true
		}
	}
	return false
}

func (parser *HTMLParser) nodeToTagAwareString(node *html.Node, stripLinebreak bool) string {
	strs := parser.nodeToTaggedStrings(node, stripLinebreak)
	var output strings.Builder
	for _, str := range strs {
		tstr := str.string
		if parser.isBlockTag(str.tag) {
			tstr = fmt.Sprintf("\n%s\n", tstr)
		}
		output.WriteString(tstr)
	}
	return strings.TrimSpace(output.String())
}

func (parser *HTMLParser) nodeToStrings(node *html.Node, stripLinebreak bool) (strs []string) {
	for ; node != nil; node = node.NextSibling {
		strs = append(strs, parser.singleNodeToString(node, stripLinebreak).string)
	}
	return
}

func (parser *HTMLParser) nodeToString(node *html.Node, stripLinebreak bool) string {
	return strings.Join(parser.nodeToStrings(node, stripLinebreak), "")
}

func (parser *HTMLParser) Parse(htmlData string) string {
	if parser.TabsToSpaces >= 0 {
		htmlData = strings.Replace(htmlData, "\t", strings.Repeat(" ", parser.TabsToSpaces), -1)
	}
	node, _ := html.Parse(strings.NewReader(htmlData))
	return parser.nodeToTagAwareString(node, true)
}

func HTMLToText(html string) string {
	return (&HTMLParser{
		TabsToSpaces: 4,
		Newline:      "\n",
	}).Parse(html)
}
