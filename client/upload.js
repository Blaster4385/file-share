
document.getElementById('uploadForm').addEventListener('submit', async (event) => {
    event.preventDefault();

    const fileInput = document.getElementById('fileInput');
    const file = fileInput.files[0];
    
    if (!file) {
        console.log('No file selected.');
        return;
    }

    const formData = new FormData();
    formData.append('file', file);

    try {
        const response = await fetch('http://localhost:8080/upload', {
            method: 'POST',
            body: formData
        });

        const info1h = document.getElementById('info1h');
        const info2h = document.getElementById('info2h');

        if (response.ok) {
            const contentType = response.headers.get('Content-Type');
            
            if (contentType && contentType.includes('application/json')) {
                const result = await response.json();
                info1h.innerText = "id=" + result.id;
                info2h.innerText = "key=" + result.key;
            } else {
                const result = await response.text();
                info1h.innerText = result;
                info2h.innerText = "";
            }

            info1h.classList.remove('info1');
            info1h.classList.add('info1v');

            info2h.classList.remove('info2');
            info2h.classList.add('info2v');
        } else {
            info1h.innerText = response.statusText;
            info1h.classList.remove('info1');
            info1h.classList.add('info1v');
        }
    } catch (error) {
        console.error('Error:', error);
    }
});
