from flask import Flask, request, render_template, redirect, url_for, session, send_file
import redis
from fpdf import FPDF
import json

app = Flask(__name__)
app.secret_key = 'supersecretkey'
r = redis.Redis(host='localhost', port=6379, db=0)

LANGUAGES = {
    'ru': {
        'welcome': 'Добро пожаловать!',
        'start_test': 'Начать тест',
        'submit_test': 'Завершить тест',
        'results': 'Результаты теста',
    },
    'en': {
        'welcome': 'Welcome!',
        'start_test': 'Start Test',
        'submit_test': 'Submit Test',
        'results': 'Test Results',
    }
}

@app.route('/set_language/<lang>')
def set_language(lang):
    if lang in LANGUAGES:
        session['language'] = lang
    return redirect(url_for('test', uuid=request.args.get('uuid')))

@app.route('/test/<uuid>')
def test(uuid):
    lang = session.get('language', 'ru')
    translations = LANGUAGES.get(lang, LANGUAGES['ru'])
    return render_template('test.html', uuid=uuid, translations=translations)

@app.route('/start_test', methods=['POST'])
def start_test():
    if 'name' not in request.form or 'surname' not in request.form or 'group' not in request.form or 'uuid' not in request.form:
        return "Ошибка: Не все данные были отправлены.", 400

    name = request.form['name']
    surname = request.form['surname']
    patronymic = request.form.get('patronymic', '')
    group = request.form['group']
    test_uuid = request.form['uuid']

    # Сохраняем имя и фамилию в сессии
    session['name'] = name
    session['surname'] = surname

    student_data = {
        'name': name,
        'surname': surname,
        'patronymic': patronymic,
        'group': group
    }
    r.hset(f"student:{name}:{surname}", mapping=student_data)

    return redirect(url_for('solve_test', uuid=test_uuid))

@app.route('/solve_test/<uuid>')
def solve_test(uuid):
    # Извлекаем вопросы
    questions = r.smembers(f"user:0:test:{uuid}:questions")
    questions_list = [question.decode('utf-8') for question in questions]

    # Извлекаем ответы для каждого вопроса
    answers_dict = {}
    for i, question in enumerate(questions_list):
        answers = r.smembers(f"user:0:test:{uuid}:answers:{i}")  # ключ для каждого вопроса
        answers_list = [answer.decode('utf-8') for answer in answers]
        answers_dict[i] = answers_list  # привязываем ответы к индексу вопроса

    name = session.get('name', 'Unknown')
    surname = session.get('surname', 'Unknown')

    return render_template('solve_test.html', uuid=uuid, questions=questions_list, answers_dict=answers_dict, name=name, surname=surname)

@app.route('/submit_test', methods=['POST'])
def submit_test():
    test_uuid = request.form['uuid']
    user_answers = {}

    for key, value in request.form.items():
        if key.startswith('question_'):
            question_index = key.replace('question_', '')
            user_answers[question_index] = value

    results = {}
    correct_count = 0
    for i, (question_index, user_answer) in enumerate(user_answers.items()):
        correct_answers = r.smembers(f"user:0:test:{test_uuid}:answers:{question_index}")
        correct_answer = correct_answers.pop().decode('utf-8') if correct_answers else None

        is_correct = user_answer == correct_answer
        results[str(i)] = {
            'user_answer': user_answer,
            'correct_answer': correct_answer,
            'is_correct': is_correct
        }
        if is_correct:
            correct_count += 1

    results_serialized = {k: json.dumps(v) for k, v in results.items()}
    r.hset(f"test:{test_uuid}:results", mapping=results_serialized)

    # Получаем имя и фамилию из сессии
    name = session.get('name', 'Unknown')
    surname = session.get('surname', 'Unknown')

    user_key = f"user:{name}:{surname}:rating"
    r.incrby(user_key, correct_count)

    return redirect(url_for('results', uuid=test_uuid))

@app.route('/results/<uuid>')
def results(uuid):
    questions = r.smembers(f"user:0:test:{uuid}:questions")
    results = r.hgetall(f"test:{uuid}:results")

    questions_list = [question.decode('utf-8') for question in questions]
    results_list = {k.decode('utf-8'): json.loads(v.decode('utf-8')) for k, v in results.items()}

    correct_count = sum(1 for result in results_list.values() if result['is_correct'])
    total_questions = len(questions_list)

    if total_questions > 0:
        score = (correct_count / total_questions) * 100
    else:
        score = 0

    return render_template('results.html', uuid=uuid, questions=questions_list, results=results_list, score=score, correct_count=correct_count, total_questions=total_questions)

@app.route('/download_results/<uuid>')
def download_results(uuid):
    questions = r.smembers(f"user:0:test:{uuid}:questions")
    results = r.hgetall(f"test:{uuid}:results")

    questions_list = [question.decode('utf-8') for question in questions]
    results_list = {k.decode('utf-8'): json.loads(v.decode('utf-8')) for k, v in results.items()}

    pdf = FPDF()
    pdf.add_page()
    pdf.set_font("Arial", size=12)

    pdf.cell(200, 10, txt="Результаты теста", ln=True, align='C')
    for i, question in enumerate(questions_list):
        pdf.cell(200, 10, txt=f"Вопрос {i+1}: {question}", ln=True)
        pdf.cell(200, 10, txt=f"Ваш ответ: {results_list[str(i)]['user_answer']}", ln=True)
        pdf.cell(200, 10, txt=f"Правильный ответ: {results_list[str(i)]['correct_answer']}", ln=True)
        pdf.cell(200, 10, txt="", ln=True)

    pdf.output("results.pdf")
    return send_file("results.pdf", as_attachment=True)

@app.route('/stats/<name>/<surname>')
def stats(name, surname):
    user_key = f"user:{name}:{surname}:rating"
    rating = r.get(user_key)
    if rating:
        rating = int(rating.decode('utf-8'))
    else:
        rating = 0

    return render_template('stats.html', name=name, surname=surname, rating=rating)

if __name__ == '__main__':
    app.run(debug=True, port=5001)