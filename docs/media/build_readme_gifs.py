#!/usr/bin/env python3
"""Build annotated, looping GIF tours from the committed UI screenshots.

Usage from any directory: python docs/media/build_readme_gifs.py
Requires Pillow. These are screenshot tours, not recordings of live interactions.
The screenshot pixels and displayed product data are never retouched.
"""

from pathlib import Path
import argparse
import os

from PIL import Image, ImageDraw, ImageFont, ImageOps


ROOT = Path(__file__).resolve().parents[2]
IMAGES = ROOT / "docs" / "img"
WIDTH, HEIGHT = 1280, 964
BG = "#F3F0FA"
INK = "#241A38"
PURPLE = "#8B2CF5"
MUTED = "#6F647D"
FONT_NAMES = {
    "regular": ["segoeui.ttf", "DejaVuSans.ttf"],
    "bold": ["segoeuib.ttf", "DejaVuSans-Bold.ttf"],
}


def font(size, bold=False):
    kind = "bold" if bold else "regular"
    roots = [Path(os.environ.get("WINDIR", "C:/Windows")) / "Fonts",
             Path("/usr/share/fonts/truetype/dejavu")]
    for folder in roots:
        for name in FONT_NAMES[kind]:
            path = folder / name
            if path.is_file():
                return ImageFont.truetype(str(path), size)
    raise RuntimeError("Install Segoe UI or DejaVu Sans (Cyrillic support is required).")


TOURS = {
    "wb-monitor-overview": ("ВАШ WB ПОД НАБЛЮДЕНИЕМ", [
        ("panel-overview", "Вся работа — на одном экране",
         "Задания, статусы и последние наблюдения в локальной панели.", "Обзор"),
        ("panel-results", "Товары и конкуренты — в одной таблице",
         "Найдите артикул. Сравните цену, позицию и остатки в выбранном регионе.", "Результаты"),
        ("panel-track", "История вместо ручных проверок",
         "Собирайте регулярно и следите за ценой и местом товара в поиске.", "Графики"),
        ("panel-export", "Данные готовы к работе",
         "Выгрузите выбранные колонки в Excel, CSV, JSON или базу.", "Выгрузка"),
    ]),
    "wb-monitor-quickstart": ("ПЕРВОЕ ЗАДАНИЕ ПО ШАГАМ", [
        ("panel-settings", "01 / Подключите BlankTrail",
         "Настройки → адрес и ключ API → сохранить → проверить соединение.", "Подключение"),
        ("panel-channels", "02 / Выберите канал выхода",
         "Прямое соединение, список прокси, ротируемый прокси или шлюзы BlankTrail.", "Канал"),
        ("panel-job-new", "03 / Задайте поисковую фразу",
         "Задачи → добавить задание → поисковая выдача по фразе.", "Фраза"),
        ("panel-regions", "04 / Укажите регион покупателя",
         "Выберите область, город и пункт выдачи в справочнике.", "Регион"),
        ("panel-job-fields", "05 / Настройте объём и поля",
         "Начните с малого: 1–3 страницы, 4 потока, запуск по запросу.", "Поля"),
    ]),
    "wb-monitor-workflow": ("ОТ АССОРТИМЕНТА К ДЕЙСТВИЮ", [
        ("panel-profile", "Ваш магазин начинается с одной ссылки",
         "Мой профиль → карточка товара → ассортимент, фразы и конкуренты.", "Профиль"),
        ("panel-jobs", "Сбор по вашему расписанию",
         "Видно, какие задания идут, что завершилось и где были отказы.", "Задачи"),
        ("panel-track", "Следите за изменениями",
         "Цена и позиции на графиках. Новые наблюдения пополняют историю.", "История"),
        ("panel-rules", "Получайте нужные уведомления",
         "Задайте событие и порог. Подключите Telegram для доставки сообщений.", "Сигналы"),
        ("panel-export", "Передайте результат своей команде",
         "Фильтры и выбранные поля сохраняются в удобный формат выгрузки.", "Экспорт"),
    ]),
}


def scene(eyebrow, steps, current):
    name, title, caption, _ = steps[current]
    canvas = Image.new("RGB", (WIDTH, HEIGHT), BG)
    draw = ImageDraw.Draw(canvas)
    draw.rectangle((0, 0, WIDTH, 130), fill=INK)
    draw.rectangle((0, 0, 8, 130), fill=PURPLE)
    draw.text((32, 17), "WB Monitor от BlankTrail", font=font(16, True), fill="#DCC2FF")
    label_font = font(14)
    label_width = draw.textlength(eyebrow, font=label_font)
    draw.text((WIDTH - 32 - label_width, 19), eyebrow, font=label_font, fill="#DCC2FF")
    draw.text((32, 52), title, font=font(31, True), fill="white")
    draw.text((33, 98), caption, font=font(18), fill="#E5DAF4")

    # Fit the entire real screenshot; no fake clicks, charts or data.
    shot = Image.open(IMAGES / f"{name}.png").convert("RGB")
    shot = ImageOps.contain(shot, (1232, 770), Image.Resampling.LANCZOS)
    left = (WIDTH - shot.width) // 2
    top = 150 + (770 - shot.height) // 2
    draw.rounded_rectangle((left - 2, top - 2, left + shot.width + 2,
                            top + shot.height + 2), radius=4, fill="#DED3EC")
    canvas.paste(shot, (left, top))

    # Navigation doubles as an accessible explanation of the tour's order.
    draw = ImageDraw.Draw(canvas)
    step_width = (WIDTH - 64) / len(steps)
    for i, item in enumerate(steps):
        x = int(32 + i * step_width)
        draw.rounded_rectangle((x, 929, x + 22, 951), radius=11,
                               fill=PURPLE if i == current else "#DFD5EA")
        draw.text((x + 7, 931), str(i + 1), font=font(12, True),
                  fill="white" if i == current else MUTED)
        draw.text((x + 30, 930), item[3], font=font(15, i == current),
                  fill=INK if i == current else MUTED)
    return canvas


def build(name, eyebrow, steps):
    scenes = [scene(eyebrow, steps, i) for i in range(len(steps))]
    # One shared palette prevents text/background flicker across GIF frames.
    swatches = Image.new("RGB", (640, 240 * len(scenes)), BG)
    for i, canvas in enumerate(scenes):
        swatches.paste(canvas.resize((640, 240)), (0, i * 240))
    palette = swatches.quantize(colors=256, method=Image.Quantize.MEDIANCUT)
    frames = []
    for canvas in scenes:
        # Twelve small progress updates over 4.8 seconds; long enough to read.
        for tick in range(12):
            frame = canvas.copy()
            draw = ImageDraw.Draw(frame)
            draw.rectangle((0, 126, WIDTH, 129), fill="#4D365F")
            draw.rectangle((0, 126, round(WIDTH * (tick + 1) / 12), 129), fill="#B875FF")
            frames.append(frame.quantize(palette=palette, dither=Image.Dither.NONE))
    output = IMAGES / f"{name}.gif"
    frames[0].save(output, save_all=True, append_images=frames[1:],
                   duration=400, loop=0, optimize=True, disposal=1,
                   comment=b"Screenshot tour; source: committed docs/img/panel-*.png")
    print(f"{output.name}: {len(frames)} frames, {len(frames) * 0.4:.1f}s, "
          f"{output.stat().st_size / 1024:.0f} KiB")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tour", choices=list(TOURS), help="Regenerate one GIF only")
    args = parser.parse_args()
    for name, (eyebrow, steps) in TOURS.items():
        if args.tour is None or args.tour == name:
            build(name, eyebrow, steps)
