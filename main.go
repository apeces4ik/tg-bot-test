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
	TOKEN = "8003937278:AAGAdeoAQjVilYGCH0hEeYV9yS5tk-pH2i8"
)

type Storage struct {
	redisClient *redis.Client
}

func NewStorage() *Storage {
	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})
	return &Storage{redisClient: rdb}
}

type Router struct {
	bot       *tgbotapi.BotAPI
	storage   *Storage
	userState map[int64]string
	userData  map[int64]map[string]string
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

		if update.Message == nil {
			continue
		}

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

		chatID := update.Message.Chat.ID
		state := r.userState[chatID]

		log.Printf("Обработка сообщения от пользователя %d. Текущее состояние: %s", chatID, state)

		switch state {
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

	log.Printf("Пользователь %d начал работу с ботом.", chatID)

	subjects, err := r.getSubjects(chatID)
	if err != nil {
		log.Printf("Ошибка при получении дисциплин для пользователя %d: %v", chatID, err)
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup()
	keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("Создать дисциплину", "create_subject"),
	))

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

	log.Printf("Пользователь %d создает дисциплину: %s", chatID, subjectName)

	err := r.saveSubject(chatID, subjectName)
	if err != nil {
		log.Printf("Ошибка при сохранении дисциплины для пользователя %d: %v", chatID, err)
		reply := tgbotapi.NewMessage(chatID, "Ошибка при сохранении дисциплины.")
		r.bot.Send(reply)
		return
	}

	delete(r.userState, chatID)
	r.handleStart(msg)
}

func (r *Router) saveSubject(chatID int64, subjectName string) error {
	ctx := context.Background()
	key := fmt.Sprintf("user:%d:subjects", chatID)
	log.Printf("Сохранение дисциплины %s для пользователя %d", subjectName, chatID)
	_, err := r.storage.redisClient.SAdd(ctx, key, subjectName).Result()
	return err
}

func (r *Router) getSubjects(chatID int64) ([]string, error) {
	ctx := context.Background()
	key := fmt.Sprintf("user:%d:subjects", chatID)
	log.Printf("Получение дисциплин для пользователя %d", chatID)
	return r.storage.redisClient.SMembers(ctx, key).Result()
}

func (r *Router) handleCreateTest(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	testName := msg.Text

	log.Printf("Пользователь %d создает тест: %s", chatID, testName)

	if r.userState[chatID] != "ожидание названия теста" {
		log.Printf("Неверное состояние для пользователя %d: %s", chatID, r.userState[chatID])
		reply := tgbotapi.NewMessage(chatID, "Неверное состояние. Используйте /start для начала.")
		r.bot.Send(reply)
		return
	}

	testUUID := uuid.New().String()

	err := r.saveTest(chatID, r.userData[chatID]["subject"], testName, testUUID)
	if err != nil {
		log.Printf("Ошибка при сохранении теста для пользователя %d: %v", chatID, err)
		reply := tgbotapi.NewMessage(chatID, "Ошибка при сохранении теста.")
		r.bot.Send(reply)
		return
	}

	if r.userData[chatID] == nil {
		r.userData[chatID] = make(map[string]string)
	}
	r.userData[chatID]["testUUID"] = testUUID
	r.userData[chatID]["test"] = testName

	r.userState[chatID] = "ожидание количества вопросов"

	log.Printf("Тест %s успешно создан. UUID: %s", testName, testUUID)

	reply := tgbotapi.NewMessage(chatID, "Введите количество вопросов:")
	r.bot.Send(reply)
}

func (r *Router) saveTest(chatID int64, subjectName, testName, testUUID string) error {
	ctx := context.Background()
	key := fmt.Sprintf("user:%d:subject:%s:tests", chatID, subjectName)
	log.Printf("Сохранение теста %s для дисциплины %s пользователя %d", testName, subjectName, chatID)
	_, err := r.storage.redisClient.SAdd(ctx, key, testName).Result()
	if err != nil {
		return err
	}

	uuidKey := fmt.Sprintf("user:%d:test:%s:uuid", chatID, testName)
	_, err = r.storage.redisClient.Set(ctx, uuidKey, testUUID, 0).Result()
	return err
}

func (r *Router) handleSetQuestionCount(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	questionCount, err := strconv.Atoi(msg.Text)
	if err != nil || questionCount < 1 {
		log.Printf("Некорректное количество вопросов от пользователя %d: %s", chatID, msg.Text)
		delete(r.userState, chatID)
		delete(r.userData, chatID)

		reply := tgbotapi.NewMessage(chatID, "Пожалуйста, введите корректное число (больше 0). Используйте /start для начала.")
		r.bot.Send(reply)
		return
	}

	r.userData[chatID]["questionCount"] = msg.Text
	r.userState[chatID] = "ожидание вопроса"

	// Инициализируем currentQuestion для первого вопроса
	r.userData[chatID]["currentQuestion"] = "0"

	log.Printf("Пользователь %d установил количество вопросов: %d", chatID, questionCount)

	reply := tgbotapi.NewMessage(chatID, "Введите текст первого вопроса:")
	r.bot.Send(reply)
}
func (r *Router) handleCreateQuestion(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	questionText := msg.Text

	log.Printf("Пользователь %d создает вопрос: %s", chatID, questionText)

	testUUID := r.userData[chatID]["testUUID"]
	if testUUID == "" {
		log.Printf("Ошибка: testUUID пуст для пользователя %d", chatID)
		return
	}

	err := r.saveQuestion(testUUID, questionText)
	if err != nil {
		log.Printf("Ошибка при сохранении вопроса для пользователя %d: %v", chatID, err)
		reply := tgbotapi.NewMessage(chatID, "Ошибка при сохранении вопроса.")
		r.bot.Send(reply)
		return
	}

	r.userState[chatID] = "ожидание количества ответов"

	log.Printf("Вопрос успешно сохранен. Переход к вводу ответов для вопроса %s.", r.userData[chatID]["currentQuestion"])

	reply := tgbotapi.NewMessage(chatID, "Введите количество вариантов ответов:")
	r.bot.Send(reply)
}

func (r *Router) saveQuestion(testUUID, questionText string) error {
	ctx := context.Background()
	key := fmt.Sprintf("user:0:test:%s:questions", testUUID)
	log.Printf("Сохранение вопроса: %s для теста с UUID: %s", questionText, testUUID)
	_, err := r.storage.redisClient.SAdd(ctx, key, questionText).Result()
	return err
}

func (r *Router) handleSetAnswerCount(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	answerCount, err := strconv.Atoi(msg.Text)
	if err != nil || answerCount < 1 {
		log.Printf("Некорректное количество ответов от пользователя %d: %s", chatID, msg.Text)
		delete(r.userState, chatID)
		delete(r.userData, chatID)

		reply := tgbotapi.NewMessage(chatID, "Пожалуйста, введите корректное число (больше 0). Используйте /start для начала.")
		r.bot.Send(reply)
		return
	}

	r.userData[chatID]["answerCount"] = msg.Text
	r.userState[chatID] = "ожидание ответа"

	log.Printf("Пользователь %d установил количество ответов: %d", chatID, answerCount)

	reply := tgbotapi.NewMessage(chatID, "Введите правильный ответ:")
	r.bot.Send(reply)
}

func (r *Router) handleAddAnswer(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	answer := msg.Text

	log.Printf("Пользователь %d добавляет ответ: %s", chatID, answer)

	testUUID := r.userData[chatID]["testUUID"]
	if testUUID == "" {
		log.Printf("Ошибка: testUUID пуст для пользователя %d", chatID)
		return
	}

	questionIndex := r.userData[chatID]["currentQuestion"]
	if questionIndex == "" {
		log.Printf("Ошибка: currentQuestion пуст для пользователя %d", chatID)
		return
	}

	log.Printf("Текущий индекс вопроса: %s, количество оставшихся ответов: %s", questionIndex, r.userData[chatID]["answerCount"])

	err := r.saveAnswer(testUUID, questionIndex, answer)
	if err != nil {
		log.Printf("Ошибка при сохранении ответа для пользователя %d: %v", chatID, err)
		return
	}

	answerCount, _ := strconv.Atoi(r.userData[chatID]["answerCount"])
	answerCount--
	r.userData[chatID]["answerCount"] = strconv.Itoa(answerCount)

	if answerCount > 0 {
		log.Printf("Осталось ответов для текущего вопроса: %d", answerCount)
		reply := tgbotapi.NewMessage(chatID, "Введите следующий ответ:")
		r.bot.Send(reply)
	} else {
		questionCount, _ := strconv.Atoi(r.userData[chatID]["questionCount"])
		questionCount--
		r.userData[chatID]["questionCount"] = strconv.Itoa(questionCount)

		if questionCount > 0 {
			// Увеличиваем индекс вопроса
			currentQuestionIndex, _ := strconv.Atoi(r.userData[chatID]["currentQuestion"])
			currentQuestionIndex++
			r.userData[chatID]["currentQuestion"] = strconv.Itoa(currentQuestionIndex)

			log.Printf("Переход к следующему вопросу. Новый индекс вопроса: %s, оставшиеся вопросы: %s", r.userData[chatID]["currentQuestion"], r.userData[chatID]["questionCount"])

			r.userState[chatID] = "ожидание вопроса"
			reply := tgbotapi.NewMessage(chatID, "Введите текст следующего вопроса:")
			r.bot.Send(reply)
		} else {
			delete(r.userState, chatID)
			delete(r.userData, chatID)

			testLink := fmt.Sprintf("http://127.0.0.1:5001/test/%s", testUUID)
			log.Printf("Тест успешно создан. Ссылка для прохождения: %s", testLink)
			reply := tgbotapi.NewMessage(chatID, fmt.Sprintf("Тест успешно создан! Ссылка для прохождения: %s", testLink))
			r.bot.Send(reply)
		}
	}
}
func (r *Router) saveAnswer(testUUID, questionIndex, answerText string) error {
	ctx := context.Background()
	key := fmt.Sprintf("user:0:test:%s:answers:%s", testUUID, questionIndex)
	log.Printf("Сохранение ответа: %s для вопроса %s теста с UUID: %s", answerText, questionIndex, testUUID)
	_, err := r.storage.redisClient.SAdd(ctx, key, answerText).Result()
	if err != nil {
		log.Printf("Ошибка при сохранении ответа в Redis: %v", err)
	}
	return err
}

func (r *Router) handleCallbackQuery(query *tgbotapi.CallbackQuery) {
	chatID := query.Message.Chat.ID
	data := query.Data

	log.Printf("Пользователь %d нажал на кнопку: %s", chatID, data)

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
	tests, err := r.getTests(chatID, subjectName)
	if err != nil {
		log.Printf("Ошибка при получении тестов для пользователя %d: %v", chatID, err)
		reply := tgbotapi.NewMessage(chatID, "Ошибка при получении тестов.")
		r.bot.Send(reply)
		return
	}

	if len(tests) == 0 {
		log.Printf("Для дисциплины %s пользователя %d нет тестов.", subjectName, chatID)
		reply := tgbotapi.NewMessage(chatID, "В этой дисциплине пока нет тестов.")
		r.bot.Send(reply)
		return
	}

	reply := tgbotapi.NewMessage(chatID, fmt.Sprintf("Тесты в дисциплине '%s':\n- %s", subjectName, strings.Join(tests, "\n- ")))
	r.bot.Send(reply)
}

func (r *Router) getTests(chatID int64, subjectName string) ([]string, error) {
	ctx := context.Background()
	key := fmt.Sprintf("user:%d:subject:%s:tests", chatID, subjectName)
	log.Printf("Получение тестов для дисциплины %s пользователя %d", subjectName, chatID)
	return r.storage.redisClient.SMembers(ctx, key).Result()
}

func (r *Router) handleDeleteSubject(chatID int64, subjectName string) {
	err := r.deleteSubject(chatID, subjectName)
	if err != nil {
		log.Printf("Ошибка при удалении дисциплины для пользователя %d: %v", chatID, err)
		reply := tgbotapi.NewMessage(chatID, "Ошибка при удалении дисциплины.")
		r.bot.Send(reply)
		return
	}

	log.Printf("Дисциплина %s успешно удалена для пользователя %d", subjectName, chatID)
	reply := tgbotapi.NewMessage(chatID, fmt.Sprintf("Дисциплина '%s' успешно удалена.", subjectName))
	r.bot.Send(reply)

	msg := &tgbotapi.Message{
		Chat: &tgbotapi.Chat{
			ID: chatID,
		},
	}
	r.handleStart(msg)
}

func (r *Router) deleteSubject(chatID int64, subjectName string) error {
	ctx := context.Background()

	key := fmt.Sprintf("user:%d:subjects", chatID)
	log.Printf("Удаление дисциплины %s для пользователя %d", subjectName, chatID)
	_, err := r.storage.redisClient.SRem(ctx, key, subjectName).Result()
	if err != nil {
		return err
	}

	testKey := fmt.Sprintf("user:%d:subject:%s:tests", chatID, subjectName)
	tests, err := r.storage.redisClient.SMembers(ctx, testKey).Result()
	if err != nil {
		return err
	}

	for _, test := range tests {
		questionKey := fmt.Sprintf("user:%d:test:%s:questions", chatID, test)
		answerKey := fmt.Sprintf("user:%d:test:%s:answers", chatID, test)
		r.storage.redisClient.Del(ctx, questionKey)
		r.storage.redisClient.Del(ctx, answerKey)
	}

	r.storage.redisClient.Del(ctx, testKey)

	return nil
}

func (r *Router) handleUnknownCommand(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	log.Printf("Неизвестная команда от пользователя %d", chatID)

	delete(r.userState, chatID)
	delete(r.userData, chatID)

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
