package firebase

import (
	"context"
	"fmt"

	"kelimeler-backend/internal/models"

	firebase "firebase.google.com/go/v4"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// FetchDictionaryWords, Firestore'daki dictionaryWords koleksiyonunun tamamını okur.
// Yalnızca bu koleksiyon okunur; kullanıcı verisine dokunulmaz.
func FetchDictionaryWords(ctx context.Context, projectID, serviceAccountPath string) ([]models.DictionaryWord, error) {
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: projectID},
		option.WithCredentialsFile(serviceAccountPath))
	if err != nil {
		return nil, fmt.Errorf("firebase app oluşturulamadı: %w", err)
	}

	client, err := app.Firestore(ctx)
	if err != nil {
		return nil, fmt.Errorf("firestore client oluşturulamadı: %w", err)
	}
	defer client.Close()

	iter := client.Collection("dictionaryWords").Documents(ctx)
	defer iter.Stop()

	var words []models.DictionaryWord
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("doküman okuma hatası: %w", err)
		}
		data := doc.Data()
		words = append(words, models.DictionaryWord{
			ID:        doc.Ref.ID, // orijinal doc id (ör. "A1_1_1")
			English:   asString(data["english"]),
			Turkish:   asString(data["turkish"]),
			Example:   asString(data["example"]),
			Level:     asString(data["level"]),
			Grp:       asInt(data["group"]),
			WordOrder: asInt(data["order"]),
		})
	}
	return words, nil
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// asInt, Firestore'dan int64/float64/int gelebilen sayısal alanları normalize eder.
func asInt(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}
