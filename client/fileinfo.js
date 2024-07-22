
const fileInfo = document.querySelector("#fileInfo");
let link = document.getElementById('link')
async function getID() {
  const id = document.getElementById('idInput').value;
  const apiKey = document.getElementById('keyInput').value;

  const baseURL1 = 'http://localhost:8080/get';
  const baseURL2 = 'http://localhost:8080/download';
  const url = `${baseURL1}/${id}?key=${apiKey}`;
  link.href =`${baseURL2}/${id}?key=${apiKey}` ;
  try {
    let response = await fetch(url);


    let data = await response.json(); 
  
     fileName = data.fileName;

      fileSize = data.fileSize;

    
    fileInfo.innerHTML = `<b>File Name:</b> ${fileName}<br><br></b><b>File Size:</> </b>${fileSize}`;
    fileInfo.style.display = 'block';
    
  } 
  
  catch (error) {
  console.error('Error fetching data:', error);
    
  }
}




