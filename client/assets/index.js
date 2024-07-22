document.addEventListener('DOMContentLoaded', async () => {
    const urlParams = new URLSearchParams(window.location.search);
    const fileId = urlParams.get('id');
    const key = urlParams.get('key');

    if (fileId && key) {
        displayFileDetails(fileId, key);
    } else {
        document.getElementById('upload__form').style.display = 'block';
        setupUploadForm();
    }
});

const baseUrl = window.location.origin;

async function displayFileDetails(fileId, key) {
    try {
        const response = await fetch(`${baseUrl}/get/${fileId}?key=${key}`, {
            method: 'GET',
        });

        const fileDetails = document.getElementById('file__details');
        const fileNameElement = document.getElementById('file__name');
        const fileSizeElement = document.getElementById('file__size');
        const downloadButton = document.getElementById('download__btn');
        const copyButton = document.getElementById('copy__btn');

        if (!response.ok) {
            fileDetails.textContent = `Error: ${response.statusText}`;
            fileDetails.style.display = 'flex';
            return;
        }

        const contentType = response.headers.get('Content-Type');
        
        if (contentType && contentType.includes('application/json')) {
            const result = await response.json();
            const downloadUrl = `${baseUrl}/download/${fileId}?key=${key}`;
            const pageUrl = `${baseUrl}/?id=${fileId}&key=${key}`;
            fileNameElement.textContent = `${result.fileName}`;
            fileSizeElement.textContent = `${result.fileSize}`;
            downloadButton.innerHTML = `<a href="${downloadUrl}">Download</a>`;
            copyButton.onclick = () => {
                navigator.clipboard.writeText(pageUrl);
                copyButton.textContent = 'Copied!';
            };
        } else {
            const result = await response.text();
            fileDetails.textContent = result;
        }

        fileDetails.style.display = 'flex';
    } catch (error) {
        console.error('Error:', error);
        document.getElementById('file__details').textContent = 'An error occurred. Please try again.';
        document.getElementById('file__details').style.display = 'flex';
    }
}

function setupUploadForm() {
    const fileInput = document.querySelector('.upload__input');
    const overlayText = document.querySelector('.upload__input__overlay__text');

    fileInput.addEventListener('change', () => {
        if (fileInput.files.length > 0) {
            overlayText.textContent = fileInput.files[0].name;
        } else {
            overlayText.textContent = 'Choose a file or drag it here';
        }
    });

    document.getElementById('upload__form').addEventListener('submit', async (event) => {
        event.preventDefault();

        const file = fileInput.files[0];

        if (!file) {
            console.log('No file selected.');
            return;
        }

        const formData = new FormData();
        formData.append('file', file);

        try {
            const response = await fetch(`${baseUrl}/upload`, {
                method: 'POST',
                body: formData
            });

            const uploadResult = document.getElementById('upload__result');

            if (!response.ok) {
                uploadResult.textContent = `Error: ${response.statusText}`;
                uploadResult.classList.add('upload__result__visible');
                return;
            }

            const contentType = response.headers.get('Content-Type');
            
            if (contentType && contentType.includes('application/json')) {
                const result = await response.json();
                const pageUrl = `${baseUrl}/?id=${result.id}&key=${result.key}`;
                window.location.href = pageUrl;
            } else {
                const result = await response.text();
                uploadResult.textContent = result;
            }

            uploadResult.classList.add('upload__result__visible');
        } catch (error) {
            console.error('Error:', error);
            document.getElementById('upload__result').textContent = 'An error occurred. Please try again.';
            document.getElementById('upload__result').classList.add('upload__result__visible');
        }
    });
}