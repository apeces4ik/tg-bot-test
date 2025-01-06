from flask import Flask, request, render_template, redirect, url_for
import redis

app = Flask(__name__)
r = redis.Redis(host='localhost', port=6379, db=0)

@app.route('/test/<uuid>')
def test(uuid):
    # Отображаем форму для ввода данных ученика
    return render_template('test.html', uuid=uuid)

@app.route('/start_test', methods=['POST'])
def start_test():
    # Получаем данные ученика
    name = request.form['name']
    surname = request.form['surname']
    patronymic = request.form['patronymic']
    group = request.form['group']
    test_uuid = request.form['uuid']

    # Сохраняем данные ученика в Redis
    student_data = {
        'name': name,
        'surname': surname,
        'patronymic': patronymic,
        'group': group
    }
    r.hset(f"student:{name}:{surname}", mapping=student_data)

    # Перенаправляем на страницу с вопросами теста
    return redirect(url_for('solve_test', uuid=test_uuid))

@app.route('/solve_test/<uuid>')
def solve_test(uuid):
    # Получаем вопросы и ответы из Redis
    questions = r.smembers(f"user:0:test:{uuid}:questions")
    answers = r.smembers(f"user:0:test:{uuid}:answers")

    # Преобразуем данные из Redis в списки
    questions_list = [question.decode('utf-8') for question in questions]
    answers_list = [answer.decode('utf-8') for answer in answers]

    # Отображаем страницу с вопросами теста
    return render_template('solve_test.html', uuid=uuid, questions=questions_list, answers=answers_list)
@app.route('/submit_test', methods=['POST'])
def submit_test():
    # Получаем ответы ученика
    test_uuid = request.form['uuid']
    user_answers = {}

    # Получаем ответы пользователя
    for key, value in request.form.items():
        if key.startswith('question_'):
            question_index = key.replace('question_', '')
            user_answers[question_index] = value

    # Получаем правильные ответы из Redis
    correct_answers_redis = r.smembers(f"user:0:test:{test_uuid}:answers")
    correct_answers_list = [answer.decode('utf-8') for answer in correct_answers_redis]

    # Сравниваем ответы пользователя с правильными
    results = {}
    for i, (question, user_answer) in enumerate(user_answers.items()):
        correct_answer = correct_answers_list[i]
        results[question] = {
            'user_answer': user_answer,
            'correct_answer': correct_answer,
            'is_correct': user_answer == correct_answer
        }

    # Сохраняем результаты в Redis
    r.hset(f"test:{test_uuid}:results", mapping=results)

    # Перенаправляем на страницу с результатами
    return redirect(url_for('results', uuid=test_uuid))

@app.route('/results/<uuid>')
def results(uuid):
    # Получаем вопросы и результаты из Redis
    questions = r.smembers(f"user:0:test:{uuid}:questions")
    results = r.hgetall(f"test:{uuid}:results")

    # Преобразуем данные из Redis в списки
    questions_list = [question.decode('utf-8') for question in questions]
    results_list = {k.decode('utf-8'): v.decode('utf-8') for k, v in results.items()}

    # Отображаем страницу с результатами
    return render_template('results.html', uuid=uuid, questions=questions_list, results=results_list)

if __name__ == '__main__':
    app.run(debug=True, port=5001)