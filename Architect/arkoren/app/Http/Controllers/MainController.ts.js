import { User } from '../../User.ts';
export var MainController;
(function (MainController) {
    function index() {
        return { name: 'Erik', age: 22 };
    }
    MainController.index = index;
    function another() {
        const name = (new User).getTable();
        return `<h1>User model table: ${name}</h1>`;
    }
    MainController.another = another;
    function book({ params }) {
        return `<h1>Book: ${params.name}, page ${params.page}</h1>`;
    }
    MainController.book = book;
})(MainController || (MainController = {}));
//# sourceMappingURL=MainController.js.map