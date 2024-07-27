document.addEventListener('DOMContentLoaded', async () => {
  const urlParams = new URLSearchParams(window.location.search)
  const fileId = urlParams.get('id')
  const key = urlParams.get('key')

  if (fileId && key) {
    displayFileDetails(fileId, key)
  } else {
    document.getElementById('upload__form').style.display = 'block'
    setupUploadForm()
  }
})

const baseUrl = window.location.origin
const CHUNK_SIZE = 100 * 1024 * 1024 // 100 MB

async function displayFileDetails(fileId, key) {
  try {
    const response = await fetch(`${baseUrl}/get/${fileId}?key=${key}`, {
      method: 'GET'
    })

    const fileDetails = document.getElementById('file__details')
    const fileNameElement = document.getElementById('file__name')
    const fileSizeElement = document.getElementById('file__size')
    const downloadButton = document.getElementById('download__btn')
    const copyButton = document.getElementById('copy__btn')

    if (!response.ok) {
      fileDetails.textContent = `Error: ${response.statusText}`
      fileDetails.style.display = 'flex'
      return
    }

    const contentType = response.headers.get('Content-Type')

    if (contentType && contentType.includes('application/json')) {
      const result = await response.json()
      const downloadUrl = `${baseUrl}/download/${fileId}?key=${key}`
      const pageUrl = `${baseUrl}/?id=${fileId}&key=${key}`
      fileNameElement.textContent = `${result.fileName}`
      fileSizeElement.textContent = `${result.fileSize}`
      downloadButton.innerHTML = `<a href="${downloadUrl}">Download</a>`
      copyButton.onclick = () => {
        navigator.clipboard.writeText(pageUrl)
        copyButton.textContent = 'Copied!'
      }
    } else {
      const result = await response.text()
      fileDetails.textContent = result
    }

    fileDetails.style.display = 'flex'
  } catch (error) {
    console.error('Error:', error)
    document.getElementById('file__details').textContent =
      'An error occurred. Please try again.'
    document.getElementById('file__details').style.display = 'flex'
  }
}

function setupUploadForm() {
  const fileInput = document.querySelector('.upload__input')
  const overlayText = document.querySelector('.upload__input__overlay__text')

  fileInput.addEventListener('change', () => {
    if (fileInput.files.length > 0) {
      overlayText.textContent = fileInput.files[0].name
    } else {
      overlayText.textContent = 'Choose a file or drag it here'
    }
  })

  document
    .getElementById('upload__form')
    .addEventListener('submit', async (event) => {
      event.preventDefault()

      const file = fileInput.files[0]

      if (!file) {
        console.log('No file selected.')
        return
      }

      const progressBar = document.getElementById('upload__progress')
      const progressFill = document.getElementById('progress__fill')
      const uploadButton = document.getElementById('upload__btn')

      uploadButton.style.display = 'none'
      progressBar.style.display = 'block'
      fileInput.disabled = true

      try {
        await uploadFileInChunks(file, progressFill)
      } catch (error) {
        console.error('Error:', error)
        document.getElementById('upload__result').textContent =
          'An error occurred. Please try again.'
        document
          .getElementById('upload__result')
          .classList.add('upload__result__visible')
      } finally {
        progressBar.style.display = 'none'
        uploadButton.style.display = 'inline-block'
        fileInput.disabled = false
      }
    })
}

async function uploadFileInChunks(file, progressFill) {
  const fileSize = file.size
  const chunkCount = Math.ceil(fileSize / CHUNK_SIZE)
  const uploadId = generateUploadId()
  let uploadedSize = 0

  for (let chunkIndex = 0; chunkIndex < chunkCount; chunkIndex++) {
    const start = chunkIndex * CHUNK_SIZE
    const end = Math.min(start + CHUNK_SIZE, fileSize)
    const chunk = file.slice(start, end)

    const formData = new FormData()
    formData.append('chunk', chunk)
    formData.append('uploadId', uploadId)
    formData.append('chunkIndex', chunkIndex)
    formData.append('chunkCount', chunkCount)
    formData.append('fileName', file.name)

    await uploadChunk(formData, progressFill, uploadedSize, fileSize)

    uploadedSize += chunk.size
  }

  // Call upload_complete endpoint
  const completeFormData = new FormData()
  completeFormData.append('uploadId', uploadId)
  completeFormData.append('chunkCount', chunkCount)
  completeFormData.append('fileName', file.name)

  const completeResponse = await fetch(`${baseUrl}/upload_complete`, {
    method: 'POST',
    body: completeFormData
  })

  if (!completeResponse.ok) {
    throw new Error(`Error completing upload: ${completeResponse.statusText}`)
  }

  const result = await completeResponse.json()
  const pageUrl = `${baseUrl}/?id=${result.id}&key=${result.key}`
  window.location.href = pageUrl
}

async function uploadChunk(formData, progressFill, uploadedSize, fileSize) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', `${baseUrl}/upload_chunk`, true)

    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) {
        const totalUploaded = uploadedSize + event.loaded
        const progress = Math.round((totalUploaded / fileSize) * 100)
        progressFill.style.width = `${progress}%`
      }
    }

    xhr.onload = () => {
      if (xhr.status === 200) {
        resolve()
      } else {
        reject(new Error(`Error uploading chunk: ${xhr.statusText}`))
      }
    }

    xhr.onerror = () => reject(new Error('Network error occurred'))

    xhr.send(formData)
  })
}

function generateUploadId() {
  return Math.random().toString(36).substr(2, 9)
}
