for(const button of document.querySelectorAll('button')){
  button.onclick=()=>{
    for(const option of document.querySelectorAll('button'))option.setAttribute('aria-pressed',String(option===button));
    document.getElementById('total').textContent=button.textContent==='Express'?'$32.00':'$24.00';
  };
}
