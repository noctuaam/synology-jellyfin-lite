/* NAS legacy webOS photo viewer. ES5 syntax is intentional for Chrome 53. */
(function () {
    'use strict';
    var closeCurrent = null;

    window.NASLegacyPhotoViewer = function (options) {
        if (closeCurrent) closeCurrent();
        var items = options.items || [];
        if (!items.length) return Promise.resolve();
        var index = Math.max(0, Math.min(options.startIndex || 0, items.length - 1));
        var autoplay = !!(options.autoplay || options.shuffle);
        var timer = null;
        var generation = 0;
        var hideTimer = null;
        var controlsVisible = true;
        var failed = false;
        var closed = false;
        var buttons = [];
        var previousFocus = document.activeElement;
        var host = document.createElement('div');
        host.setAttribute('role', 'dialog');
        host.setAttribute('aria-modal', 'true');
        host.tabIndex = -1;
        host.setAttribute('aria-label', 'Просмотр фотографии');
        host.setAttribute('data-nas-photo-viewer', 'legacy');
        host.style.cssText = 'position:fixed;left:0;top:0;right:0;bottom:0;z-index:2147483646;background:#050709;color:#fff;text-align:center;font-family:Arial,sans-serif;outline:none;';
        var style = document.createElement('style');
        style.textContent = '[data-nas-photo-viewer] button{font:17px Arial,sans-serif;color:#eef6ff;background:rgba(29,43,60,.88);border:1px solid rgba(190,217,240,.24);border-radius:28px;margin:0 6px;padding:13px 20px;cursor:pointer;transition:background .15s;}[data-nas-photo-viewer] button:hover{background:#344d66;}[data-nas-photo-viewer] button:focus{outline:3px solid #65d6ff;outline-offset:3px;}[data-nas-photo-viewer] button:disabled{opacity:.3;cursor:default;}';
        host.appendChild(style);
        var frame = document.createElement('div');
        frame.style.cssText = 'position:absolute;left:0;right:0;top:0;bottom:0;display:flex;align-items:center;justify-content:center;';
        var top = document.createElement('div');
        top.style.cssText = 'position:absolute;left:0;right:0;top:0;padding:30px 40px 65px;text-align:left;background:linear-gradient(rgba(0,0,0,.8),transparent);pointer-events:none;transition:opacity .25s;';
        var counter = document.createElement('div');
        counter.style.cssText = 'font-size:13px;letter-spacing:2px;color:#9fdcf1;margin-bottom:9px;';
        var title = document.createElement('div');
        title.style.cssText = 'font-size:23px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;text-shadow:0 1px 4px #000;';
        top.appendChild(counter);
        top.appendChild(title);
        var bar = document.createElement('div');
        bar.setAttribute('role', 'toolbar');
        bar.setAttribute('aria-label', 'Управление просмотром');
        bar.style.cssText = 'position:absolute;left:0;right:0;bottom:0;padding:60px 20px 28px;background:linear-gradient(transparent,rgba(0,0,0,.88));transition:opacity .25s;';
        var actions = document.createElement('div');
        actions.style.cssText = 'display:flex;align-items:center;justify-content:center;';
        var hint = document.createElement('div');
        hint.textContent = '← → Фото     ↑ Управление     OK Слайд-шоу     Назад Закрыть';
        hint.style.cssText = 'font-size:13px;color:#b0bccb;margin-top:20px;';
        bar.appendChild(actions);
        bar.appendChild(hint);
        var message = document.createElement('div');
        message.setAttribute('role', 'status');
        message.style.cssText = 'position:absolute;left:15%;right:15%;top:45%;font-size:18px;line-height:1.6;text-shadow:0 1px 4px #000;pointer-events:none;';
        var image = null;
        var frozenFrame = null;

        function button(label, action) {
            var element = document.createElement('button');
            element.textContent = label;
            
            element.addEventListener('click', function () { reveal(); action(); });
            buttons.push(element);
            actions.appendChild(element);
            return element;
        }

        function reveal() {
            if (closed) return;
            controlsVisible = true;
            host.setAttribute('data-controls-visible', 'true');
            host.style.cursor = '';
            top.style.opacity = bar.style.opacity = '1';
            bar.style.pointerEvents = 'auto';
            bar.setAttribute('aria-hidden', 'false');
            for (var i = 0; i < buttons.length; i++) buttons[i].tabIndex = 0;
            clearTimeout(hideTimer);
            if (!failed) hideTimer = setTimeout(hide, 3500);
        }

        function hide() {
            if (closed || failed) return;
            controlsVisible = false;
            host.setAttribute('data-controls-visible', 'false');
            if (bar.contains(document.activeElement)) host.focus();
            host.style.cursor = 'none';
            top.style.opacity = bar.style.opacity = '0';
            bar.style.pointerEvents = 'none';
            bar.setAttribute('aria-hidden', 'true');
            for (var i = 0; i < buttons.length; i++) buttons[i].tabIndex = -1;
        }

        function schedule() {
            clearTimeout(timer);
            if (autoplay && index < items.length - 1) {
                timer = setTimeout(function () { show(index + 1); }, 11000);
            }
        }

        function originalURL(item) {
            var tag = item.ImageTags && item.ImageTags.Primary;
            return '/Items/' + encodeURIComponent(item.Id) + '/Images/Primary?tag=' + encodeURIComponent(tag || '');
        }

        function clearFrozenFrame() {
            if (!frozenFrame) return;
            if (frozenFrame.parentNode) frozenFrame.parentNode.removeChild(frozenFrame);
            frozenFrame.width = frozenFrame.height = 0;
            frozenFrame = null;
        }

        function keepPreviousFrame() {
            if (!image || !image.complete || !image.naturalWidth) return;
            clearFrozenFrame();
            try {
                var width = window.innerWidth || 1920;
                var height = window.innerHeight || 1080;
                var scale = Math.min(1, 1280 / width, 720 / height);
                var canvas = document.createElement('canvas');
                canvas.width = Math.max(1, Math.round(width * scale));
                canvas.height = Math.max(1, Math.round(height * scale));
                var context = canvas.getContext('2d');
                if (!context) return;
                var fit = Math.min(1, width / image.naturalWidth, height / image.naturalHeight) * scale;
                var drawWidth = image.naturalWidth * fit;
                var drawHeight = image.naturalHeight * fit;
                context.drawImage(image, (canvas.width - drawWidth) / 2, (canvas.height - drawHeight) / 2, drawWidth, drawHeight);
                canvas.style.cssText = 'position:absolute;left:0;top:0;width:100%;height:100%;pointer-events:none;';
                canvas.setAttribute('aria-hidden', 'true');
                frame.appendChild(canvas);
                frozenFrame = canvas;
            } catch (error) {
                // A drawing failure must not prevent normal original-image loading.
                clearFrozenFrame();
            }
        }

        function show(nextIndex) {
            if (nextIndex < 0 || nextIndex >= items.length) return;
            index = nextIndex;
            failed = false;
            previousButton.disabled = index === 0;
            nextButton.disabled = index === items.length - 1;
            retryButton.style.display = 'none';
            counter.textContent = 'ФОТО ' + (index + 1) + ' / ' + items.length;
            title.textContent = items[index].Name || 'Фотография';
            clearTimeout(timer);
            generation += 1;
            var currentGeneration = generation;
            keepPreviousFrame();
            if (image) {
                image.onload = image.onerror = null;
                image.removeAttribute('src');
                frame.removeChild(image);
            }
            image = document.createElement('img');
            image.alt = '';
            image.style.cssText = 'display:block;max-width:100%;max-height:100%;width:auto;height:auto;opacity:0;';
            var item = items[index];
            message.textContent = '';
            image.onload = function () {
                if (currentGeneration !== generation) return;
                image.style.opacity = '1';
                clearFrozenFrame();
                message.textContent = '';
                if (controlsVisible) reveal();
                schedule();
            };
            image.onerror = function () {
                if (currentGeneration !== generation) return;
                failed = true;
                autoplay = false;
                retryButton.style.display = '';
                reveal();
                playButton.textContent = '▶ Слайд-шоу';
                message.textContent = 'Не удалось показать оригинал. Нажмите «Повторить» или перейдите к следующему фото.';
            };
            frame.appendChild(image);
            // No sizing or thumbnail parameters: this is always the original.
            image.src = originalURL(item);
        }

        function toggle() {
            autoplay = !autoplay;
            playButton.textContent = autoplay ? 'Ⅱ Пауза' : '▶ Слайд-шоу';
            if (image && image.complete && image.naturalWidth) schedule();
            else if (!autoplay) clearTimeout(timer);
        }

        function close() {
            closed = true;
            generation += 1;
            clearTimeout(hideTimer);
            clearTimeout(timer);
            window.removeEventListener('keydown', keydown, true);
            if (image) {
                image.onload = image.onerror = null;
                image.removeAttribute('src');
            }
            clearFrozenFrame();
            if (host.parentNode) host.parentNode.removeChild(host);
            if (previousFocus && previousFocus.focus) previousFocus.focus();
            closeCurrent = null;
        }

        function keydown(event) {
            var key = event.keyCode;
            var wasVisible = controlsVisible;
            var target = document.activeElement;
            reveal();
            if ([37, 39, 38, 40, 8, 27, 461, 13, 415, 19, 179].indexOf(key) === -1) return;
            event.preventDefault();
            event.stopImmediatePropagation();
            if (key === 8 || key === 27 || key === 461) { close(); return; }
            if (key === 38) { playButton.focus(); return; }
            if (key === 40) { host.focus(); return; }
            if (key === 37 || key === 39) {
                if (wasVisible && actions.contains(target)) {
                    var position = buttons.indexOf(target);
                    do { position += key === 37 ? -1 : 1; }
                    while (position >= 0 && position < buttons.length && (buttons[position].disabled || buttons[position].style.display === 'none'));
                    if (position >= 0 && position < buttons.length) buttons[position].focus();
                } else show(index + (key === 37 ? -1 : 1));
            } else if (key === 13 && wasVisible && actions.contains(target)) target.click();
            else if (key !== 13 || wasVisible) {
                if (key === 415) autoplay = false;
                if (key === 19) autoplay = true;
                toggle();
            }
        }

        var previousButton = button('← Предыдущее', function () { show(index - 1); });
        var playButton = button(autoplay ? 'Ⅱ Пауза' : '▶ Слайд-шоу', toggle);
        var nextButton = button('Следующее →', function () { show(index + 1); });
        var retryButton = button('Повторить', function () { show(index); });
        button('✕ Закрыть', close);
        host.appendChild(frame);
        host.appendChild(top);
        host.appendChild(bar);
        host.appendChild(message);
        document.body.appendChild(host);
        window.addEventListener('keydown', keydown, true);
        closeCurrent = close;
        host.addEventListener('mousemove', reveal);
        host.addEventListener('touchstart', reveal);
        host.addEventListener('click', reveal);
        host.addEventListener('wheel', reveal);
        bar.addEventListener('focusin', reveal);
        host.focus();
        show(index);
        reveal();
        return Promise.resolve();
    };
}());
