package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/go-redis/redis/v8"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/uuid"
)

const (
	TOKEN = "7950145009:AAHCp_yMQYr8mOFWXe8N2dB5fCv02bfLY-w" // Замените на ваш токен
)

type Storage struct {
	redisClient *redis.Client
}

func NewStorage() *Storage {
	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379", // Адрес Redis сервера
		Password: "",               // Пароль, если есть
		DB:       0,                // Номер базы данных
	})
	return &Storage{redisClient: rdb}
}

type Router struct {
	bot       *tgbotapi.BotAPI
	storage   *Storage
	userState map[int64]string            // Состояние пользователя
	userData  map[int64]map[string]string // Временные данные пользователя
}

func NewRouter(bot *tgbotapi.BotAPI, storage *Storage) *Router {
	return &Router{
		bot:       bot,
		storage:   storage,
		userState: make(map[int64]string),
		userData:  make(map[int64]map[string]string),
	}
}

func (r *Router) HandleUpdates(updates tgbotapi.UpdatesChannel) {
	for update := range updates {
		if update.CallbackQuery != nil {
			r.handleCallbackQuery(update.CallbackQuery)
			continue
		}

		if update.Message == nil { // Игнорируем любые не-сообщения
			continue
		}

		// Обрабатываем команду /start
		if update.Message.IsCommand() {
			switch update.Message.Command() {
			case "start":
				r.handleStart(update.Message)
				continue
			default:
				r.handleUnknownCommand(update.Message)
				continue
			}
		}

		// Обрабатываем текстовые сообщения в зависимости от состояния
		switch r.userState[update.Message.Chat.ID] {
		case "ожидание названия дисциплины":
			r.handleCreateSubject(update.Message)
		case "ожидание названия теста":
			r.handleCreateTest(update.Message)
		case "ожидание количества вопросов":
			r.handleSetQuestionCount(update.Message)
		case "ожидание вопроса":
			r.handleCreateQuestion(update.Message)
		case "ожидание количества ответов":
			r.handleSetAnswerCount(update.Message)
		case "ожидание ответа":
			r.handleAddAnswer(update.Message)
		default:
			r.handleUnknownCommand(update.Message)
		}
	}
}

func (r *Router) handleStart(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	// Получаем список дисциплин
	subjects, err := r.getSubjects(chatID)
	if err != nil {
		log.Println("Ошибка при получении дисциплин:", err)
	}

	// Создаем клавиатуру с кнопками
	keyboard := tgbotapi.NewInlineKeyboardMarkup()
	keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("Создать дисциплину", "create_subject"),
	))

	// Добавляем кнопки для каждой дисциплины
	for _, subject := range subjects {
		keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(subject, "select_subject:"+subject),
		))
	}

	reply := tgbotapi.NewMessage(chatID, "Добро пожаловать! Выберите или создайте новую дисциплину.")
	reply.ReplyMarkup = keyboard
	r.bot.Send(reply)
}

func (r *Router) handleCreateSubject(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	subjectName := msg.Text

	// Сохраняем дисциплину в Redis
	err := r.saveSubject(chatID, subjectName)
	if err != nil {
		reply := tgbotapi.NewMessage(chatID, "Ошибка при сохранении дисциплины.")
		r.bot.Send(reply)
		return
	}

	// Сбрасываем состояние пользователя
	delete(r.userState, chatID)

	// Показываем обновлённый список дисциплин
	r.handleStart(msg)
}

func (r *Router) handleCreateTest(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	testName := msg.Text

	// Генерируем уникальный идентификатор для теста
	testUUID := uuid.New().String()

	// Сохраняем тест в Redis
	err := r.saveTest(chatID, r.userData[chatID]["subject"], testName, testUUID)
	if err != nil {
		reply := tgbotapi.NewMessage(chatID, "Ошибка при сохранении теста.")
		r.bot.Send(reply)
		return
	}

	// Сохраняем testUUID в userData
	if r.userData[chatID] == nil {
		r.userData[chatID] = make(map[string]string)
	}
	r.userData[chatID]["testUUID"] = testUUID
	r.userData[chatID]["test"] = testName

	// Переходим к вводу количества вопросов
	r.userState[chatID] = "ожидание количества вопросов"

	reply := tgbotapi.NewMessage(chatID, "Введите количество вопросов:")
	r.bot.Send(reply)
}

func (r *Router) handleSetQuestionCount(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	questionCount, err := strconv.Atoi(msg.Text)
	if err != nil || questionCount < 1 {
		reply := tgbotapi.NewMessage(chatID, "Пожалуйста, введите корректное число (больше 0).")
		r.bot.Send(reply)
		return
	}

	// Сохраняем количество вопросов
	r.userData[chatID]["questionCount"] = msg.Text
	r.userState[chatID] = "ожидание вопроса"

	reply := tgbotapi.NewMessage(chatID, "Введите текст первого вопроса:")
	r.bot.Send(reply)
}

func (r *Router) handleCreateQuestion(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	questionText := msg.Text

	// Сохраняем вопрос в Redis
	err := r.saveQuestion(chatID, r.userData[chatID]["testUUID"], questionText)
	if err != nil {
		reply := tgbotapi.NewMessage(chatID, "Ошибка при сохранении вопроса.")
		r.bot.Send(reply)
		return
	}

	// Переходим к вводу количества ответов
	r.userState[chatID] = "ожидание количества ответов"

	reply := tgbotapi.NewMessage(chatID, "Введите количество вариантов ответов:")
	r.bot.Send(reply)
}

func (r *Router) handleSetAnswerCount(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	answerCount, err := strconv.Atoi(msg.Text)
	if err != nil || answerCount < 1 {
		reply := tgbotapi.NewMessage(chatID, "Пожалуйста, введите корректное число (больше 0).")
		r.bot.Send(reply)
		return
	}

	// Сохраняем количество ответов
	r.userData[chatID]["answerCount"] = msg.Text
	r.userState[chatID] = "ожидание ответа"

	reply := tgbotapi.NewMessage(chatID, "Введите правильный ответ:")
	r.bot.Send(reply)
}

func (r *Router) handleAddAnswer(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	answer := msg.Text

	// Сохраняем ответ в Redis
	err := r.saveAnswer(chatID, r.userData[chatID]["testUUID"], answer)
	if err != nil {
		reply := tgbotapi.NewMessage(chatID, "Ошибка при сохранении ответа.")
		r.bot.Send(reply)
		return
	}

	// Уменьшаем количество оставшихся ответов
	answerCount, _ := strconv.Atoi(r.userData[chatID]["answerCount"])
	answerCount--
	r.userData[chatID]["answerCount"] = strconv.Itoa(answerCount)

	if answerCount > 0 {
		reply := tgbotapi.NewMessage(chatID, "Введите следующий ответ:")
		r.bot.Send(reply)
	} else {
		// Переходим к следующему вопросу или завершаем
		questionCount, _ := strconv.Atoi(r.userData[chatID]["questionCount"])
		questionCount--
		r.userData[chatID]["questionCount"] = strconv.Itoa(questionCount)

		if questionCount > 0 {
			r.userState[chatID] = "ожидание вопроса"
			reply := tgbotapi.NewMessage(chatID, "Введите текст следующего вопроса:")
			r.bot.Send(reply)
		} else {
			// Сохраняем testUUID перед очисткой userData
			testUUID := r.userData[chatID]["testUUID"]
			if testUUID == "" {
				reply := tgbotapi.NewMessage(chatID, "Ошибка: UUID теста не найден.")
				r.bot.Send(reply)
				return
			}

			// Сбрасываем состояние пользователя
			delete(r.userState, chatID)
			delete(r.userData, chatID)

			// Отправляем ссылку на тест
			testLink := fmt.Sprintf("http://127.0.0.1:5001/test/%s", testUUID)
			reply := tgbotapi.NewMessage(chatID, fmt.Sprintf("Тест успешно создан! Ссылка для прохождения: %s", testLink))
			r.bot.Send(reply)
		}
	}
}

func (r *Router) handleCallbackQuery(query *tgbotapi.CallbackQuery) {
	chatID := query.Message.Chat.ID
	data := query.Data

	switch {
	case data == "create_subject":
		r.userState[chatID] = "ожидание названия дисциплины"
		reply := tgbotapi.NewMessage(chatID, "Введите название дисциплины:")
		r.bot.Send(reply)
	case strings.HasPrefix(data, "select_subject:"):
		subjectName := strings.TrimPrefix(data, "select_subject:")
		r.userData[chatID] = map[string]string{"subject": subjectName}
		r.handleSelectSubject(chatID, subjectName)
	case strings.HasPrefix(data, "create_test:"):
		subjectName := strings.TrimPrefix(data, "create_test:")
		r.userState[chatID] = "ожидание названия теста"
		r.userData[chatID] = map[string]string{"subject": subjectName}
		reply := tgbotapi.NewMessage(chatID, "Введите название теста:")
		r.bot.Send(reply)
	case strings.HasPrefix(data, "view_tests:"):
		subjectName := strings.TrimPrefix(data, "view_tests:")
		r.handleViewTests(chatID, subjectName)
	case strings.HasPrefix(data, "delete_subject:"):
		subjectName := strings.TrimPrefix(data, "delete_subject:")
		r.handleDeleteSubject(chatID, subjectName)
	case data == "back_to_subjects":
		r.handleStart(query.Message)
	default:
		r.handleUnknownCommand(query.Message)
	}
}

func (r *Router) handleSelectSubject(chatID int64, subjectName string) {
	// Создаем клавиатуру с действиями для дисциплины
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Создать тест", "create_test:"+subjectName),
			tgbotapi.NewInlineKeyboardButtonData("Просмотр всех тестов", "view_tests:"+subjectName),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Удалить дисциплину", "delete_subject:"+subjectName),
			tgbotapi.NewInlineKeyboardButtonData("Назад к списку дисциплин", "back_to_subjects"),
		),
	)

	reply := tgbotapi.NewMessage(chatID, fmt.Sprintf("Вы выбрали дисциплину: %s. Выберите действие:", subjectName))
	reply.ReplyMarkup = keyboard
	r.bot.Send(reply)
}

func (r *Router) handleViewTests(chatID int64, subjectName string) {
	// Получаем список тестов для дисциплины
	tests, err := r.getTests(chatID, subjectName)
	if err != nil {
		reply := tgbotapi.NewMessage(chatID, "Ошибка при получении тестов.")
		r.bot.Send(reply)
		return
	}

	if len(tests) == 0 {
		reply := tgbotapi.NewMessage(chatID, "В этой дисциплине пока нет тестов.")
		r.bot.Send(reply)
		return
	}

	// Показываем список тестов
	reply := tgbotapi.NewMessage(chatID, fmt.Sprintf("Тесты в дисциплине '%s':\n- %s", subjectName, strings.Join(tests, "\n- ")))
	r.bot.Send(reply)
}

func (r *Router) handleDeleteSubject(chatID int64, subjectName string) {
	// Удаляем дисциплину и все связанные данные
	err := r.deleteSubject(chatID, subjectName)
	if err != nil {
		reply := tgbotapi.NewMessage(chatID, "Ошибка при удалении дисциплины.")
		r.bot.Send(reply)
		return
	}

	// Показываем обновлённый список дисциплин
	reply := tgbotapi.NewMessage(chatID, fmt.Sprintf("Дисциплина '%s' успешно удалена.", subjectName))
	r.bot.Send(reply)

	// Создаем пустое сообщение с ChatID для handleStart
	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{
			ID: chatID,
		},
	}
	r.handleStart(msg)
}

func (r *Router) deleteSubject(chatID int64, subjectName string) error {
	ctx := context.Background()

	// Удаляем дисциплину
	key := fmt.Sprintf("user:%d:subjects", chatID)
	_, err := r.storage.redisClient.SRem(ctx, key, subjectName).Result()
	if err != nil {
		return err
	}

	// Удаляем все тесты, связанные с дисциплиной
	testKey := fmt.Sprintf("user:%d:subject:%s:tests", chatID, subjectName)
	tests, err := r.storage.redisClient.SMembers(ctx, testKey).Result()
	if err != nil {
		return err
	}

	for _, test := range tests {
		// Удаляем вопросы и ответы для каждого теста
		questionKey := fmt.Sprintf("user:%d:test:%s:questions", chatID, test)
		answerKey := fmt.Sprintf("user:%d:test:%s:answers", chatID, test)
		r.storage.redisClient.Del(ctx, questionKey)
		r.storage.redisClient.Del(ctx, answerKey)
	}

	// Удаляем ключ с тестами
	r.storage.redisClient.Del(ctx, testKey)

	return nil
}

func (r *Router) saveSubject(chatID int64, subjectName string) error {
	ctx := context.Background()
	key := fmt.Sprintf("user:%d:subjects", chatID)
	_, err := r.storage.redisClient.SAdd(ctx, key, subjectName).Result()
	return err
}

func (r *Router) getSubjects(chatID int64) ([]string, error) {
	ctx := context.Background()
	key := fmt.Sprintf("user:%d:subjects", chatID)
	return r.storage.redisClient.SMembers(ctx, key).Result()
}

func (r *Router) saveTest(chatID int64, subjectName, testName, testUUID string) error {
	ctx := context.Background()
	key := fmt.Sprintf("user:%d:subject:%s:tests", chatID, subjectName)
	_, err := r.storage.redisClient.SAdd(ctx, key, testName).Result()
	if err != nil {
		return err
	}

	// Сохраняем UUID теста
	uuidKey := fmt.Sprintf("user:%d:test:%s:uuid", chatID, testName)
	_, err = r.storage.redisClient.Set(ctx, uuidKey, testUUID, 0).Result()
	return err
}

func (r *Router) getTests(chatID int64, subjectName string) ([]string, error) {
	ctx := context.Background()
	key := fmt.Sprintf("user:%d:subject:%s:tests", chatID, subjectName)
	return r.storage.redisClient.SMembers(ctx, key).Result()
}

func (r *Router) saveQuestion(_ int64, testUUID, questionText string) error {
	ctx := context.Background()
	key := fmt.Sprintf("user:0:test:%s:questions", testUUID)
	_, err := r.storage.redisClient.SAdd(ctx, key, questionText).Result()
	return err
}

func (r *Router) saveAnswer(_ int64, testUUID, answerText string) error {
	ctx := context.Background()
	key := fmt.Sprintf("user:0:test:%s:answers", testUUID)
	_, err := r.storage.redisClient.SAdd(ctx, key, answerText).Result()
	return err
}

func (r *Router) handleUnknownCommand(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	// Если пользователь в непонятном состоянии, сбрасываем его
	delete(r.userState, chatID)
	delete(r.userData, chatID)

	// Отправляем сообщение с инструкцией
	reply := tgbotapi.NewMessage(chatID, "Неизвестная команда. Используйте /start для начала.")
	r.bot.Send(reply)
}

func main() {
	bot, err := tgbotapi.NewBotAPI(TOKEN)
	if err != nil {
		log.Panic("Ошибка при создании бота:", err)
	}

	bot.Debug = true
	log.Printf("Бот запущен: %s", bot.Self.UserName)

	storage := NewStorage()
	router := NewRouter(bot, storage)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	go router.HandleUpdates(updates)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Бот выключен")
}
