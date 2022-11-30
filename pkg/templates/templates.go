package templates

import (
	"bytes"
	"encoding/base64"
	"errors"
	"html/template"
	"strings"

	"github.com/coneno/logger"
	"github.com/influenzanet/messaging-service/pkg/types"
)

func GetTemplateTranslation(tDef types.EmailTemplate, lang string) types.LocalizedTemplate {
	var defaultTranslation types.LocalizedTemplate
	for _, tr := range tDef.Translations {
		if tr.Lang == lang {
			return tr
		} else if tr.Lang == tDef.DefaultLanguage {
			defaultTranslation = tr
		}
	}
	return defaultTranslation
}

func ResolveTemplate(tempName string, templateDef string, contentInfos map[string]string) (content string, err error) {
	if strings.TrimSpace(templateDef) == "" {
		logger.Error.Printf("error: empty template %s", tempName)
		return "", errors.New("empty template `" + tempName)
	}
	tmpl, err := template.New(tempName).Parse(templateDef)
	if err != nil {
		logger.Error.Printf("error when parsing template %s: %v", tempName, err)
		return "", err
	}
	var tpl bytes.Buffer

	err = tmpl.Execute(&tpl, contentInfos)
	if err != nil {
		logger.Error.Printf("error when executing template %s: %v", tempName, err)
		return "", err
	}
	return tpl.String(), nil
}

func CheckAllTranslationsParsable(tempTranslations types.EmailTemplate) (err error) {
	if len(tempTranslations.Translations) == 0 {
		logger.Error.Printf("error when decoding template %s: translation list is empty", tempTranslations.MessageType)
		return errors.New("error when decoding template `" + tempTranslations.MessageType + "`: translation list is empty")
	}
	for _, templ := range tempTranslations.Translations {
		templateName := tempTranslations.MessageType + templ.Lang
		decodedTemplate, err := base64.StdEncoding.DecodeString(templ.TemplateDef)
		if err != nil {
			logger.Error.Printf("error when decoding template %s: %v", templateName, err)
			return err
		}
		_, err = ResolveTemplate(
			templateName,
			string(decodedTemplate),
			make(map[string]string),
		)
		if err != nil {
			return errors.New("could not parse template for `" + templ.Lang + "` - error: " + err.Error())
		}
	}
	return nil
}
